package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/capability"
)

const (
	apiSecretReference = "{{secret:secrow:X}}"

	apiSecretValue = "sk-api-secret-value"
)

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(raw)
}

type apiSecretRefs struct {
	value  string
	values []string
	err    error
	calls  *atomic.Int64
}

func (r apiSecretRefs) ResolveText(_ context.Context, text string) (string, []capability.PlacedSecret, error) {
	if !strings.Contains(text, "{{secret:") {
		return text, []capability.PlacedSecret{}, nil
	}
	call := 0
	if r.calls != nil {
		call = int(r.calls.Add(1))
	}
	if r.err != nil {
		return "", nil, r.err
	}
	if !strings.Contains(text, apiSecretReference) {
		return "", nil, &capability.UnresolvedSecretError{Reference: text}
	}
	value := r.value
	if call > 0 && call <= len(r.values) {
		value = r.values[call-1]
	}
	return strings.ReplaceAll(text, apiSecretReference, value), []capability.PlacedSecret{{Name: "secrow:X", Value: value}}, nil
}

type apiSecretReceived struct {
	mu     sync.Mutex
	header string
	path   string
	body   string
	hits   int
}
type apiSecretSnapshot struct {
	header string
	path   string
	body   string
	hits   int
}

func (r *apiSecretReceived) record(req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.header = req.Header.Get("X-Token")
	r.path = req.URL.Path
	r.body = string(body)
	r.hits++
}

func (r *apiSecretReceived) snapshot() apiSecretSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return apiSecretSnapshot{header: r.header, path: r.path, body: r.body, hits: r.hits}
}

func apiSecretHTTPServer(t *testing.T) (*httptest.Server, *apiSecretReceived) {
	t.Helper()
	got := &apiSecretReceived{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got.record(req)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func apiSecretCollection(t *testing.T, baseURL, source, mode string) (string, string) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("nocx-collection.json", `{"schemaVersion":1,"name":"acme"}`)
	relPath := "send.json"
	if mode == "folder" {
		relPath = "users/send.json"
		write("users/.variables.json", `{"variables":[{"name":"token","value":`+mustJSON(t, source)+`,"enabled":true}]}`)
	}
	fieldSource := source
	if mode == "request" || mode == "folder" || mode == "environment" {
		fieldSource = "{{token}}"
	}
	variables := ""
	if mode == "request" {
		variables = `,"variables":[{"name":"token","value":` + mustJSON(t, source) + `,"enabled":true}]`
	}
	write(relPath, `{"id":"r1","name":"send","method":"GET","url":`+mustJSON(t, baseURL)+`,`+
		`"headers":[{"name":"X-Token","value":`+mustJSON(t, fieldSource)+`,"enabled":true}]`+variables+
		`,"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	envRel := ""
	if mode == "environment" {
		envRel = "environments/dev.json"
		write(envRel, `{"name":"dev","values":{"token":`+mustJSON(t, source)+`},"route":{"kind":"direct"}}`)
	}
	return root, envRel
}

func TestAPIRequestSend_SecretReferenceResolvesInEveryScopeWithoutEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		rel  string
		env  string
	}{
		{name: "request", mode: "request", rel: "send.json"},
		{name: "folder", mode: "folder", rel: "users/send.json"},
		{name: "environment", mode: "environment", rel: "send.json", env: "environments/dev.json"},
		{name: "bare field", mode: "bare", rel: "send.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := &atomic.Int64{}
			srv, received := apiSecretHTTPServer(t)
			refs := &apiSecretRefs{value: apiSecretValue, calls: calls}
			_, conn := newAPIWSServerWithSecretRefs(t, refs)
			root, envRel := apiSecretCollection(t, srv.URL, apiSecretReference, tc.mode)
			handle := openAPICollection(t, conn, root, 1)
			resp := vaultCall(t, conn, "api.request.send", map[string]any{
				"handle": handle, "relPath": tc.rel, "envRelPath": envRel,
			}, 2)
			if resp.Error != nil {
				t.Fatalf("api.request.send: %+v", resp.Error)
			}
			got := received.snapshot()
			if got.hits != 1 || got.header != apiSecretValue {
				t.Fatalf("server received header %q in %d requests, want %q once", got.header, got.hits, apiSecretValue)
			}
			if calls.Load() == 0 {
				t.Fatal("injected SecretRefs resolver was not called")
			}
			var exchange apiSendResponse
			if err := json.Unmarshal(resp.Result, &exchange); err != nil {
				t.Fatalf("decode exchange: %v", err)
			}
			if strings.Contains(exchange.Request.Text, apiSecretValue) {
				t.Fatalf("raw request contains secret value: %s", exchange.Request.Text)
			}
			if !strings.Contains(exchange.Request.Text, "⟦secrow:X⟧") {
				t.Fatalf("raw request lacks secret chip: %s", exchange.Request.Text)
			}
		})
	}
}

func apiSecretCollectionWithTwoReferences(t *testing.T, baseURL string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "nocx-collection.json"), []byte(`{"schemaVersion":1,"name":"acme"}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	body := `{"id":"r1","name":"send","method":"GET","url":` + mustJSON(t, baseURL+`/`+apiSecretReference) +
		`,"headers":[{"name":"X-Token","value":` + mustJSON(t, apiSecretReference) + `,"enabled":true}],"body":{"kind":"none"},"auth":{"kind":"none"}}`
	if err := os.WriteFile(filepath.Join(root, "send.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return root
}

func TestAPIRequestSend_DifferentValuesForOneHandleAreBothRedacted(t *testing.T) {
	srv, received := apiSecretHTTPServer(t)
	calls := &atomic.Int64{}
	refs := &apiSecretRefs{values: []string{"old-value", "new-value"}, calls: calls}
	_, conn := newAPIWSServerWithSecretRefs(t, refs)
	root := apiSecretCollectionWithTwoReferences(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "send.json",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}
	got := received.snapshot()
	if got.hits != 1 || got.path != "/old-value" || got.header != "new-value" {
		t.Fatalf("server received path %q and header %q in %d requests, want old/new once", got.path, got.header, got.hits)
	}
	var exchange apiSendResponse
	if err := json.Unmarshal(resp.Result, &exchange); err != nil {
		t.Fatalf("decode exchange: %v", err)
	}
	if strings.Contains(exchange.Request.Text, "old-value") || strings.Contains(exchange.Request.Text, "new-value") {
		t.Fatalf("raw request leaked a secret value: %s", exchange.Request.Text)
	}
	if calls.Load() < 2 {
		t.Fatalf("resolver calls = %d, want both references resolved", calls.Load())
	}
}

func apiSecretCollectionWithBody(t *testing.T, baseURL, kind, source string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(text), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("nocx-collection.json", `{"schemaVersion":1,"name":"acme"}`)
	var bodyText string
	switch kind {
	case "form":
		bodyText = "token=" + source
	default:
		bodyText = `{"token":` + mustJSON(t, source) + `}`
	}
	write("send.json", `{"id":"r1","name":"send","method":"POST","url":`+mustJSON(t, baseURL)+
		`,"body":{"kind":`+mustJSON(t, kind)+`,"text":`+mustJSON(t, bodyText)+`},"auth":{"kind":"none"}}`)
	return root
}

func TestAPIRequestSend_SecretReferenceCoversEveryTransmittedBodyKind(t *testing.T) {
	for _, kind := range []string{"raw", "json", "form"} {
		for _, source := range []string{apiSecretReference, "{{secret:display name}}"} {
			t.Run(kind+"/"+source, func(t *testing.T) {
				srv, received := apiSecretHTTPServer(t)
				refs := &apiSecretRefs{value: apiSecretValue}
				_, conn := newAPIWSServerWithSecretRefs(t, refs)
				root := apiSecretCollectionWithBody(t, srv.URL, kind, source)
				handle := openAPICollection(t, conn, root, 1)
				resp := vaultCall(t, conn, "api.request.send", map[string]any{
					"handle": handle, "relPath": "send.json",
				}, 2)
				if resp.Error != nil {
					t.Fatalf("api.request.send: %+v", resp.Error)
				}
				var exchange apiSendResponse
				if err := json.Unmarshal(resp.Result, &exchange); err != nil {
					t.Fatalf("decode exchange: %v", err)
				}
				got := received.snapshot()
				if source == apiSecretReference {
					if got.hits != 1 || !strings.Contains(got.body, apiSecretValue) {
						t.Fatalf("server received body %q in %d requests, want resolved value once", got.body, got.hits)
					}
					if strings.Contains(exchange.Request.Text, apiSecretValue) {
						t.Fatalf("raw request contains secret value: %s", exchange.Request.Text)
					}
				} else {
					if got.hits != 0 {
						t.Fatal("server was reached for an unresolved body secret")
					}
					if exchange.Failure == nil || !strings.Contains(exchange.Failure.Reason, "display name") {
						t.Fatalf("failure = %+v, want the body reference named", exchange.Failure)
					}
				}
			})
		}
	}
}

func TestAPIRequestSend_SecretValueIsNotRescanned(t *testing.T) {
	srv, received := apiSecretHTTPServer(t)
	value := "literal-{{not-a-reference}}"
	_, conn := newAPIWSServerWithSecretRefs(t, apiSecretRefs{value: value})
	root, envRel := apiSecretCollection(t, srv.URL, apiSecretReference, "bare")
	handle := openAPICollection(t, conn, root, 1)
	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "send.json", "envRelPath": envRel,
	}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}
	if got := received.snapshot(); got.header != value {
		t.Fatalf("header = %q, want the vault bytes verbatim", got.header)
	}
}

func TestAPIRequestSend_UnresolvedSecretReferenceBlocksAndNamesIt(t *testing.T) {
	srv, received := apiSecretHTTPServer(t)
	_, conn := newAPIWSServerWithSecretRefs(t, apiSecretRefs{})
	root, envRel := apiSecretCollection(t, srv.URL, "{{secret:display name}}", "bare")
	handle := openAPICollection(t, conn, root, 1)
	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "send.json", "envRelPath": envRel,
	}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}
	var exchange apiSendResponse
	if err := json.Unmarshal(resp.Result, &exchange); err != nil {
		t.Fatalf("decode exchange: %v", err)
	}
	if received.snapshot().hits != 0 {
		t.Fatal("server was reached for an unresolved secret")
	}
	if exchange.Failure == nil || !strings.Contains(exchange.Failure.Reason, "display name") {
		t.Fatalf("failure = %+v, want the reference named", exchange.Failure)
	}
}

func TestAPIRequestSend_SecretResolverErrorIsNotUnresolved(t *testing.T) {
	srv, received := apiSecretHTTPServer(t)
	providerErr := errors.New("vault provider failed")
	_, conn := newAPIWSServerWithSecretRefs(t, apiSecretRefs{err: providerErr})
	root, envRel := apiSecretCollection(t, srv.URL, apiSecretReference, "bare")
	handle := openAPICollection(t, conn, root, 1)
	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "send.json", "envRelPath": envRel,
	}, 2)
	if resp.Error == nil || !strings.Contains(resp.Error.Message, providerErr.Error()) {
		t.Fatalf("error = %+v, want provider error", resp.Error)
	}
	if received.snapshot().hits != 0 {
		t.Fatal("server was reached after resolver failure")
	}
}
