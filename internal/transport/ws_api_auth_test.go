package transport

// api.request.send resolves the request's AUTH VARIABLE (design §8) — the
// last step of the path from a collection file to a header, and the one that
// was missing: apisend.Apply had no caller anywhere, so an auth kind other
// than "none" was refused by name and no bearer, basic or api-key request
// could be sent at all.
//
// Everything here is driven over the real socket, against a real HTTP server,
// with the REAL binding store (apibind.JSONStore over a real document store
// and a fake vault). A fake binding store would prove the handler calls
// whatever it was handed; the real one is what proves the variable a person
// bound is the value the server receives, and — the test that matters — that
// a vault identifier written into a collection file resolves to nothing
// because it is a name nobody bound, not because something inspected it.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/apibind"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
)

// ── the vault, narrowed to the three calls a binding makes ───────────────

// apiAuthVault is apibind.Secrets. It is a fake rather than a real vault
// because a real one needs a seal lifecycle this test has no part in; the
// three calls are exactly the contract apibind declares, so what is faked is
// the dependency and never the thing under test.
type apiAuthVault struct {
	mu     sync.Mutex
	n      int
	values map[credential.SecretID][]byte
}

func newAPIAuthVault() *apiAuthVault {
	return &apiAuthVault{values: map[credential.SecretID][]byte{}}
}

func (v *apiAuthVault) CreateNamed(_ context.Context, value credential.Secret, _ vault.SecretMeta) (credential.SecretID, error) {
	var raw []byte
	if err := value.Use(func(b []byte) error { raw = append([]byte(nil), b...); return nil }); err != nil {
		return "", err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.n++
	// Deliberately id-shaped: the identifier a hostile file would love to be
	// able to spell is a real one in this test.
	id := credential.SecretID("keychain:nocx-secret-" + string(rune('a'+v.n-1)))
	v.values[id] = raw
	return id, nil
}

func (v *apiAuthVault) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	raw, ok := v.values[id]
	if !ok {
		// The vault's contract: an absent id is an empty Secret and a nil
		// error.
		return credential.Secret{}, nil
	}
	return credential.NewSecretBytes(raw), nil
}

func (v *apiAuthVault) Delete(_ context.Context, id credential.SecretID) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.values, id)
	return nil
}

// ── the collection a person would have ───────────────────────────────────

// apiAuthCollection writes a collection whose request authenticates through a
// variable, plus one environment naming that variable secret. authVar is what
// the FILE says — which is the knob the hostile-file test turns.
func apiAuthCollection(t *testing.T, baseURL, authVar string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("nocx-collection.json", `{"schemaVersion":1,"name":"acme"}`)
	write("private.json", `{"id":"r1","name":"private","method":"GET","url":"{{baseUrl}}/private",`+
		`"headers":[],"query":[],"body":{"kind":"none"},`+
		`"auth":{"kind":"bearer","var":`+mustJSON(t, authVar)+`}}`)
	write("environments/dev.json", `{"name":"dev","values":{"baseUrl":`+mustJSON(t, baseURL)+`},`+
		`"secretVars":["token"],"route":{"kind":"direct"}}`)
	return root
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal %q: %v", s, err)
	}
	return string(raw)
}

// apiAuthStore builds the real binding store over a real document store.
func apiAuthStore(t *testing.T) (*apibind.JSONStore, *apiAuthVault) {
	t.Helper()
	v := newAPIAuthVault()
	return apibind.NewStore(storage.NewDocumentStore(t.TempDir()), v), v
}

// sendRaw drives api.request.send and returns BOTH the decoded result and the
// exact bytes of the frame. The bytes are the point of the first test: a
// credential that reaches the renderer at all has crossed, whether or not the
// renderer knows which field it was in.
func sendRaw(t *testing.T, conn *websocket.Conn, params map[string]any, id int) (*vaultRPCResult, []byte) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "api.request.send", "params": params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(wantWithin))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var msg vaultRPCResult
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID == id {
			return &msg, data
		}
	}
}

// ── 1. the bound variable becomes the header, and never crosses ──────────

// A person binds `token` in their `dev` environment; the file says
// `"auth":{"kind":"bearer","var":"token"}`. The server receives the header,
// and the value appears in NO frame the renderer is sent — the raw
// diagnostic shows the badge instead, which is §11.2's property arriving
// through the seam that actually resolves the credential.
func TestAPIRequestSend_ABoundAuthVariableBecomesTheHeaderAndNeverCrosses(t *testing.T) {
	const secret = "t0k3n-that-must-not-cross"

	var gotAuth atomic.Value
	gotAuth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	bindings, _ := apiAuthStore(t)
	_, conn := newAPIWSServer(t, bindings)
	root := apiAuthCollection(t, srv.URL, "token")

	if err := bindings.Bind(context.Background(),
		apibind.Key{Collection: root, Environment: "dev", Variable: "token"}, []byte(secret)); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	handle := openAPICollection(t, conn, root, 1)
	resp, frame := sendRaw(t, conn, map[string]any{
		"handle": handle, "relPath": "private.json", "envRelPath": "environments/dev.json",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}
	if got, _ := gotAuth.Load().(string); got != "Bearer "+secret {
		t.Errorf("the server received Authorization %q, want %q", got, "Bearer "+secret)
	}
	if strings.Contains(string(frame), secret) {
		t.Errorf("the bound value crossed to the renderer in the send frame: %s", frame)
	}

	var got apiSendResponse
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal send result: %v", err)
	}
	if !strings.Contains(got.Response.Raw.Request.Text, "⟦token⟧") {
		t.Errorf("the raw request reads %q; the credential's place must carry the variable's NAME",
			got.Response.Raw.Request.Text)
	}
}

// ── 2. an unbound variable blocks the send and names itself ──────────────

// `Authorization: Bearer ` is a plausible-looking request that teaches the
// wrong lesson about why it was rejected (§6.5), so the send is BLOCKED and
// the answer names the variable. Asserted where it counts: the server is
// never reached.
func TestAPIRequestSend_AnUnboundAuthVariableBlocksTheSendAndNamesItself(t *testing.T) {
	var reached atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
	}))
	defer srv.Close()

	bindings, vlt := apiAuthStore(t)
	_, conn := newAPIWSServer(t, bindings)
	root := apiAuthCollection(t, srv.URL, "token")

	handle := openAPICollection(t, conn, root, 1)
	resp, _ := sendRaw(t, conn, map[string]any{
		"handle": handle, "relPath": "private.json", "envRelPath": "environments/dev.json",
	}, 2)

	if resp.Error == nil {
		t.Fatalf("a send whose auth variable is bound nowhere succeeded: %s", resp.Result)
	}
	if !strings.Contains(resp.Error.Message, "token") {
		t.Errorf("the refusal reads %q and does not name the variable the person has to bind",
			resp.Error.Message)
	}
	// The remedy is "bind this variable", and it has to read that way. A
	// send path that never resolves auth at all refuses every one of these
	// too, with apisend.ErrAuthUnresolved — an answer that is about the
	// program rather than about anything the person can do.
	if strings.Contains(resp.Error.Message, "cannot resolve") {
		t.Errorf("the refusal reads %q: that is the sender saying auth was never resolved, "+
			"not this environment saying the variable is unbound", resp.Error.Message)
	}
	if reached.Load() != 0 {
		t.Errorf("the server was reached %d times, want 0 — no empty credential goes out", reached.Load())
	}
	if len(vlt.values) != 0 {
		t.Errorf("the vault holds %d values after a send that resolved nothing", len(vlt.values))
	}
}

// ── 3. a vault identifier in the file is just a name nobody bound ────────

// The file says `"auth":{"kind":"bearer","var":"keychain:nocx-secret-a"}` —
// a REAL identifier, minted by this test's own vault for the value the same
// person bound under `token`. §8's claim is that this buys the file nothing:
// not because a guard rejects it, but because there is no syntax in which a
// file names a secret, so an identifier is an unbound variable like any
// other misspelling.
//
// The ordinary bound path runs in the SAME test, against the same store and
// the same server, so the refusal is evidence about identifiers and not
// about a store that resolves nothing.
func TestAPIRequestSend_AVaultIdentifierInTheFileResolvesToNothing(t *testing.T) {
	var reached atomic.Int64
	var gotAuth atomic.Value
	gotAuth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		gotAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	bindings, vlt := apiAuthStore(t)
	_, conn := newAPIWSServer(t, bindings)

	// One collection, honestly bound: `token` in `dev` is worth something.
	honest := apiAuthCollection(t, srv.URL, "token")
	if err := bindings.Bind(context.Background(),
		apibind.Key{Collection: honest, Environment: "dev", Variable: "token"}, []byte("the reader's own token")); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	// The identifier that value actually has. A hostile file writing this
	// has written the best thing it could possibly know.
	var id credential.SecretID
	vlt.mu.Lock()
	for k := range vlt.values {
		id = k
	}
	vlt.mu.Unlock()
	if id == "" {
		t.Fatal("the fake vault stored nothing; the test has nothing to point at")
	}

	// The collection that arrived in a pull request.
	hostile := apiAuthCollection(t, srv.URL, string(id))

	hostileHandle := openAPICollection(t, conn, hostile, 1)
	resp, frame := sendRaw(t, conn, map[string]any{
		"handle": hostileHandle, "relPath": "private.json", "envRelPath": "environments/dev.json",
	}, 2)
	if resp.Error == nil {
		t.Fatalf("a file naming a vault identifier was sent: %s", resp.Result)
	}
	if reached.Load() != 0 {
		t.Errorf("the server was reached %d times, want 0", reached.Load())
	}
	if strings.Contains(string(frame), "the reader's own token") {
		t.Errorf("the refusal frame carries the value the identifier points at: %s", frame)
	}

	// And the ordinary path, same store, same server: the refusal above is
	// about identifiers, not about a world in which nothing resolves.
	honestHandle := openAPICollection(t, conn, honest, 3)
	ok, _ := sendRaw(t, conn, map[string]any{
		"handle": honestHandle, "relPath": "private.json", "envRelPath": "environments/dev.json",
	}, 4)
	if ok.Error != nil {
		t.Fatalf("the ordinary bound variable failed too: %+v", ok.Error)
	}
	if got, _ := gotAuth.Load().(string); got != "Bearer the reader's own token" {
		t.Errorf("the server received Authorization %q, want the bound value", got)
	}
}
