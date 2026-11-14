package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apifetch"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
)

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

func fetchE2EResultFromText(t *testing.T, text string) fetchE2EResult {
	t.Helper()
	var result fetchE2EResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatal(err)
	}
	return result
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

	h := newAskHarnessWithOpts(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepTool, Name: "fetch.url", Arguments: map[string]any{"url": feed.URL}},
		{Kind: protocol.StepAnswer, Text: "the feed item is Привет"},
		{Kind: protocol.StepEnd},
	}}, WithAgentPolicy(autonomousPolicyStore(t)), WithAgentFetcher(directFetchE2EClient()))
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

	results := []fetchE2EResult{fetchE2EResultFromText(t, h.toolResult())}
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

	firstResultPath := filepath.Join(t.TempDir(), "first-result")
	h := newAskHarnessWithOpts(t, protocol.Script{Steps: []protocol.Step{
		{Kind: protocol.StepTool, Name: "fetch.url", Arguments: map[string]any{"url": feed.URL}, ResultFile: firstResultPath},
		{Kind: protocol.StepTool, Name: "fetch.url", Arguments: map[string]any{"url": feed.URL}, ArgumentsFromResult: map[string]string{"start": "window.end", "revision": "revision"}},
		{Kind: protocol.StepAnswer, Text: marker},
		{Kind: protocol.StepEnd},
	}}, WithAgentPolicy(autonomousPolicyStore(t)), WithAgentFetcher(directFetchE2EClient()))
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

	firstRaw, err := os.ReadFile(firstResultPath) //nolint:gosec // path is generated in this test's temporary directory
	if err != nil {
		t.Fatal(err)
	}
	results := []fetchE2EResult{fetchE2EResultFromText(t, string(firstRaw)), fetchE2EResultFromText(t, h.toolResult())}
	if !results[0].Truncated || results[1].Window.Start != results[0].Window.End {
		t.Fatalf("continuation windows = %+v, want contiguous windows", results)
	}
	if results[1].Revision != results[0].Revision || results[0].Revision == "" {
		t.Fatalf("snapshot revisions = %q and %q, want one nonempty revision", results[0].Revision, results[1].Revision)
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
