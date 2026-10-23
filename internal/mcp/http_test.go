package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shady2k/nocx/internal/profile"
)

func TestRedirectStripsEveryHopIncludingSameOriginAndReturn(t *testing.T) {
	for _, chain := range []string{"same", "cross", "return"} {
		t.Run(chain, func(t *testing.T) {
			seen := make(chan http.Header, 4)
			var initialURL string
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Clone()
				if chain == "return" {
					http.Redirect(w, r, initialURL+"/final", http.StatusFound)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer target.Close()
			initial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Secret") != "custom" {
						t.Error("initial credential injection missing")
					}
					if chain == "same" {
						http.Redirect(w, r, "/final", http.StatusFound)
					} else {
						http.Redirect(w, r, target.URL, http.StatusFound)
					}
					return
				}
				seen <- r.Header.Clone()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer initial.Close()
			initialURL = initial.URL
			guard := newGuardedHTTPTransport(initial.URL, []resolvedHeader{{name: "Authorization", value: "Bearer secret"}, {name: "X-Secret", value: "custom"}}, 4096, time.Second)
			defer guard.close()
			client := &http.Client{Transport: guard, CheckRedirect: guard.CheckRedirect}
			request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, initial.URL+"/start", nil)
			for _, name := range []string{"Cookie", "Cookie2", "Mcp-Session-Id", "Mcp-Protocol-Version", "Mcp-Method", "Mcp-Name", "Mcp-Future-Secret", "Last-Event-Id", "Proxy-Authorization", "X-Other"} {
				request.Header.Set(name, "sensitive")
			}
			resp, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			closeTestResource(t, resp.Body)
			hops := 1
			if chain == "return" {
				hops = 2
			}
			for range hops {
				header := await(t, seen)
				for name := range header {
					if strings.HasPrefix(strings.ToLower(name), "mcp-") || name == "Authorization" || name == "Cookie" || name == "Cookie2" || name == "Last-Event-Id" || name == "Proxy-Authorization" || name == "X-Secret" || name == "X-Other" {
						t.Fatalf("redirect forwarded %s", name)
					}
				}
			}
		})
	}
}

func TestRedirectRefusesOriginalBodyBeforeMethodRewrite(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/target" {
				calls.Add(1)
				return
			}
			http.Redirect(w, r, "/target", status)
		}))
		guard := newGuardedHTTPTransport(server.URL, nil, 2048, time.Second)
		client := &http.Client{Transport: guard, CheckRedirect: guard.CheckRedirect}
		for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodHead} {
			request, _ := http.NewRequestWithContext(t.Context(), method, server.URL, strings.NewReader("credential-bearing-body"))
			resp, err := client.Do(request)
			if resp != nil {
				closeTestResource(t, resp.Body)
			}
			if !errors.Is(err, ErrDestinationRefused) || calls.Load() != 0 {
				t.Fatalf("%s status=%d err=%v targetCalls=%d", method, status, err, calls.Load())
			}
		}
		guard.close()
		server.Close()
	}
}

func TestHTTPResponseBoundAndLifetimeCancellation(t *testing.T) {
	body := &boundedResponseBody{reader: io.NopCloser(strings.NewReader("12345")), remaining: 4}
	raw, err := io.ReadAll(body)
	if string(raw) != "1234" || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("body=%q error=%v", raw, err)
	}
	started, exited := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(exited) }))
	defer server.Close()
	lifetime, cancel := context.WithCancel(t.Context())
	guard := newGuardedHTTPTransport(server.URL, nil, 4096, time.Minute)
	guard.lifetime = lifetime
	defer guard.close()
	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
		resp, err := (&http.Client{Transport: guard}).Do(req)
		if resp != nil {
			closeTestResource(t, resp.Body)
		}
		done <- err
	}()
	await(t, started)
	cancel()
	if err := await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	await(t, exited)
}

func TestNotificationStreamUsesSessionLifetimeDuringLaterCall(t *testing.T) {
	emit, callEntered, finishCall, streamCanceled := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "first\n")
			if flushErr := http.NewResponseController(w).Flush(); flushErr != nil {
				t.Error(flushErr)
				return
			}
			select {
			case <-emit:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, "later\n")
			if flushErr := http.NewResponseController(w).Flush(); flushErr != nil {
				t.Error(flushErr)
				return
			}
			<-r.Context().Done()
			close(streamCanceled)
			return
		}
		close(callEntered)
		select {
		case <-finishCall:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard := newGuardedHTTPTransport(server.URL, nil, 4096, time.Second)
	guard.lifetime, guard.notificationStreams = lifetime, true
	defer guard.close()
	client := &http.Client{Transport: guard}
	request, _ := http.NewRequestWithContext(lifetime, http.MethodGet, server.URL, nil)
	request.Header.Set("Accept", "text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestResource(t, response.Body)
	// Assert the actual transport-owned context, rather than racing a short
	// socket timeout. The old implementation permanently attached a deadline.
	if deadline, bounded := response.Request.Context().Deadline(); bounded {
		t.Fatalf("session stream has an absolute response deadline: %v", deadline)
	}
	if guard.policy.Inner.ResponseHeaderTimeout != time.Second {
		t.Fatal("stream establishment lost its header bound")
	}
	frame := make([]byte, len("first\n"))
	if _, err := io.ReadFull(response.Body, frame); err != nil || string(frame) != "first\n" {
		t.Fatalf("initial frame %q: %v", frame, err)
	}
	callDone := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(lifetime, http.MethodPost, server.URL, nil)
		resp, err := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		callDone <- err
	}()
	await(t, callEntered)
	close(emit)
	if _, err := io.ReadFull(response.Body, frame); err != nil || string(frame) != "later\n" {
		t.Fatalf("notification during reused call %q: %v", frame, err)
	}
	close(finishCall)
	if err := await(t, callDone); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("stream survived session cancellation")
	}
	await(t, streamCanceled)
}

func TestPaginatedCatalogIsAtomicAndBounded(t *testing.T) {
	for _, mode := range []string{"valid", "duplicate", "cycle", "oversize", "invalid-schema"} {
		t.Run(mode, func(t *testing.T) {
			var pages atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						Cursor string `json:"cursor"`
					} `json:"params"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					w.WriteHeader(400)
					return
				}
				if len(request.ID) == 0 {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				var result any
				switch request.Method {
				case "initialize":
					result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "pages", "version": "1"}}
				case "tools/list":
					pages.Add(1)
					name, next := "first", "page-two"
					if request.Params.Cursor != "" {
						name, next = "second", ""
					}
					if mode == "duplicate" {
						name = "duplicate"
					}
					if mode == "cycle" {
						next = "same"
					}
					schema := any(map[string]any{"type": "object"})
					if mode == "invalid-schema" {
						schema = map[string]any{"$dynamicRef": "#bad"}
					}
					tools := []any{map[string]any{"name": name, "inputSchema": schema}}
					if mode == "oversize" {
						tools = make([]any, 257)
						for i := range tools {
							tools[i] = map[string]any{"name": "tool", "inputSchema": schema}
						}
					}
					result = map[string]any{"tools": tools, "nextCursor": next}
				default:
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			}))
			defer server.Close()
			manager := NewManager(nil)
			defer closeTestResource(t, manager)
			catalog, err := manager.Refresh(t.Context(), activationFor(t, httpServerRecord(server.URL)))
			if mode == "valid" {
				if err != nil || len(catalog.Tools) != 2 || pages.Load() != 2 {
					t.Fatalf("catalog=%+v err=%v pages=%d", catalog, err, pages.Load())
				}
			} else if err == nil || len(catalog.Tools) != 0 {
				t.Fatalf("partial or invalid catalog escaped: %+v %v", catalog, err)
			}
		})
	}
}

func TestSuccessfulAndErrorSocketResultsRedactCredential(t *testing.T) {
	for _, isError := range []bool{false, true} {
		// #nosec G101 -- isolated redaction fixture marker, not a deployed credential.
		const secret = "fixture-only-mcp-secret"
		server := sdk.NewServer(&sdk.Implementation{Name: "echo", Version: "1"}, nil)
		server.AddTool(&sdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{IsError: isError, Content: []sdk.Content{&sdk.TextContent{Text: secret}}, StructuredContent: map[string]any{"value": secret}}, nil
		})
		fixture := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
		resolver := &testResolver{values: map[string]string{"key": secret}}
		manager := NewManager(resolver)
		record := httpServerRecord(fixture.URL)
		record.HTTP.Auth = profile.MCPHTTPAuthBearer
		record.HTTP.Bearer = &profile.MCPSecretBinding{SecretRef: "key"}
		a := discoverFor(t, manager, activationFor(t, record))
		result, err := invokeEcho(t.Context(), manager, a, "echo-secret")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		if bytes.Contains(raw, []byte(secret)) || result.IsError != isError {
			t.Fatalf("unsafe result %s", raw)
		}
		closeTestResource(t, manager)
		fixture.Close()
	}
}
