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

	"github.com/shady2k/nocx/internal/capability"
)

func apiAuthRequest(t *testing.T, baseURL, token string, variable string) string {
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
	write("nocx-collection.json", `{"schemaVersion":1,"name":"auth"}`)
	vars := ""
	if variable != "" {
		vars = `,"variables":[{"name":"token","value":` + mustJSON(t, variable) + `,"enabled":true}]`
	}
	write("send.json", `{"id":"r1","name":"send","method":"GET","url":`+mustJSON(t, baseURL)+
		`,"headers":[],"body":{"kind":"none"},"auth":{"kind":"bearer","token":`+mustJSON(t, token)+`}}`+vars)
	return root
}

func TestAPIRequestSend_AuthSecretReferenceBecomesHeaderAndIsElided(t *testing.T) {
	const value = "auth-secret-value"
	var got atomic.Value
	got.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got.Store(req.Header.Get("Authorization"))
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	_, conn := newAPIWSServerWithSecretRefs(t, apiSecretRefs{value: value})
	root := apiAuthRequest(t, srv.URL, apiSecretReference, "")
	handle := openAPICollection(t, conn, root, 1)
	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "send.json", "envRelPath": "",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}
	if gotAuth := got.Load().(string); gotAuth != "Bearer "+value {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer "+value)
	}
	var exchange apiSendResponse
	if err := json.Unmarshal(resp.Result, &exchange); err != nil {
		t.Fatalf("decode exchange: %v", err)
	}
	if strings.Contains(exchange.Request.Text, value) || !strings.Contains(exchange.Request.Text, "⟦secrow:X⟧") {
		t.Fatalf("raw request = %q, want a chip and no value", exchange.Request.Text)
	}
}

func TestAPIRequestSend_LiteralAuthTextIsUnchanged(t *testing.T) {
	const value = "literal-auth-value"
	var got atomic.Value
	got.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got.Store(req.Header.Get("Authorization"))
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	_, conn := newAPIWSServerWithSecretRefs(t, apiSecretRefs{value: "unused"})
	root := apiAuthRequest(t, srv.URL, value, "")
	handle := openAPICollection(t, conn, root, 1)
	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "send.json", "envRelPath": "",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}
	if gotAuth := got.Load().(string); gotAuth != "Bearer "+value {
		t.Fatalf("Authorization = %q, want literal", gotAuth)
	}
	var exchange apiSendResponse
	if err := json.Unmarshal(resp.Result, &exchange); err != nil {
		t.Fatalf("decode exchange: %v", err)
	}
	if !strings.Contains(exchange.Request.Text, value) {
		t.Fatalf("raw request = %q, want literal value", exchange.Request.Text)
	}
}

func TestAPIRequestSend_UnboundAuthVariableBlocksBeforeDial(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("unexpected"))
	}))
	t.Cleanup(srv.Close)

	_, conn := newAPIWSServerWithSecretRefs(t, apiSecretRefs{value: "unused"})
	root := apiAuthRequest(t, srv.URL, "{{token}}", "")
	handle := openAPICollection(t, conn, root, 1)
	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "send.json", "envRelPath": "",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}
	var exchange apiSendResponse
	if err := json.Unmarshal(resp.Result, &exchange); err != nil {
		t.Fatalf("decode exchange: %v", err)
	}
	if exchange.Failure == nil || !strings.Contains(exchange.Failure.Reason, "token") {
		t.Fatalf("failure = %+v, want token named", exchange.Failure)
	}
	if hits.Load() != 0 {
		t.Fatal("server was reached for an unresolved auth variable")
	}
}

var _ capability.SecretRefs = apiSecretRefs{}
