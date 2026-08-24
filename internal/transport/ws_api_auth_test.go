package transport

// api.request.send carries the request's AUTH TEXT (design §8, nocx-6hg2w.20)
// — the last step of the path from a collection file to a header. An auth
// field is text like every other: a literal the person pasted is SENT, and
// a `{{name}}` written into one resolves through the SAME substitution as
// the URL. There is one resolver, not two.
//
// Everything here is driven over the real socket, against a real HTTP server,
// with the REAL binding store (apibind.JSONStore over a real document store
// and a fake vault). A fake binding store would prove the handler calls
// whatever it was handed; the real one is what proves the variable a person
// bound is the value the server receives, and — the test that matters — that
// a vault identifier written into a collection file is sent as the LITERAL it
// is, not resolved, because it is text and not a name the binding answered.
//
// Design §8 still holds: a file cannot NAME a secret, because there is no
// syntax in which a file names one — an identifier typed into an auth field
// is the literal it is in the file, and the binding from a name to a stored
// value lives in the binding document.

import (
	"context"
	"encoding/json"
	"fmt"
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
	values map[credential.SecretID][]byte
}

func newAPIAuthVault() *apiAuthVault {
	return &apiAuthVault{values: map[credential.SecretID][]byte{}}
}

func (v *apiAuthVault) CreateNamed(_ context.Context, value credential.Secret, _ vault.SecretMeta) (credential.SecretID, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	id := credential.SecretID(fmt.Sprintf("sec:%d", len(v.values)))
	var got []byte
	if err := value.Use(func(b []byte) error {
		got = append([]byte(nil), b...)
		return nil
	}); err != nil {
		return "", err
	}
	v.values[id] = got
	return id, nil
}

func (v *apiAuthVault) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return credential.NewSecretBytes(v.values[id]), nil
}

func (v *apiAuthVault) Delete(_ context.Context, id credential.SecretID) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.values, id)
	return nil
}

// ── the collection a person would have ───────────────────────────────────

// apiAuthCollection writes a collection whose request authenticates through
// a variable, plus one environment naming that variable secret. authToken is
// what the FILE's `token` field says — the knob the hostile-file test turns.
func apiAuthCollection(t *testing.T, baseURL, authToken string) string {
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
		`"auth":{"kind":"bearer","token":`+mustJSON(t, authToken)+`}}`)
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
	// Every send names itself, because the method requires it: a run that
	// could not be named is a run with no Stop. These tests are about the
	// credential rather than about the token, so the helper mints one and
	// the cases below stay about what they are about.
	if _, named := params["token"]; !named {
		params["token"] = fmt.Sprintf("t-%d", id)
	}
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

// ── 1. a bound {{token}} becomes the header, and never crosses ────────────

// A person binds `token` in their `dev` environment; the file says
// `"auth":{"kind":"bearer","token":"{{token}}"}`. The server receives the
// header, and the value appears in NO frame the renderer is sent — the raw
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
	root := apiAuthCollection(t, srv.URL, "{{token}}")

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
	// The request side is on the EXCHANGE now, not inside the response —
	// the sender has it before it dials, so a run that never got an answer
	// still shows it (contracts/api.request.send).
	if !strings.Contains(got.Request.Text, "⟦token⟧") {
		t.Errorf("the raw request reads %q; the credential's place must carry the variable's NAME",
			got.Request.Text)
	}
}

// unresolvedRun decodes a send that came back as a RUN which never went out,
// and asserts the shape every one of them has: outcome failed at phase
// `compose`, no response, and the request the person wrote on it.
//
// It is a run rather than an error since nocx-pgp9c.6. The refusal is the
// same refusal — nothing goes out, the server is never reached — but it
// arrives where a person is already looking, beside every other thing they
// have sent, instead of as a sentence with no request attached to it.
func unresolvedRun(t *testing.T, resp *vaultRPCResult) apiSendResponse {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("the send answered an error rather than a run: %+v", resp.Error)
	}
	var got apiSendResponse
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal send result: %v", err)
	}
	if got.Outcome != "failed" {
		t.Fatalf("outcome = %q, want failed", got.Outcome)
	}
	if got.Response != nil {
		t.Errorf("a run that never went out carries a response: %+v", *got.Response)
	}
	if got.Failure == nil || got.Failure.Phase != "compose" {
		t.Fatalf("failure = %+v, want phase compose", got.Failure)
	}
	if got.Request.Text == "" {
		t.Error("the run carries no request text; the person cannot see what they asked for")
	}
	return got
}

// ── 1b. a literal the person pasted is SENT and shows, both halves ───────

// The owner's decision (nocx-tg9l8): the product does not hide or move a
// credential a person typed. The same file with the LITERAL in the token
// field sends it, and the raw request SHOWS it — no badge, because there is
// no variable to name it by. Both halves of the "still elided / NOT elided"
// pair are asserted here so this cannot later be read as a reversal.
func TestAPIRequestSend_ALiteralPastIntoTheBearerFieldIsSentAndShown(t *testing.T) {
	const literal = "88730fee-9a4c-4c9d-8f4c-a1b2c3d4e5f6"

	var gotAuth atomic.Value
	gotAuth.Store("")
	var reached atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		gotAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	bindings, _ := apiAuthStore(t)
	_, conn := newAPIWSServer(t, bindings)
	root := apiAuthCollection(t, srv.URL, literal)

	handle := openAPICollection(t, conn, root, 1)
	resp, frame := sendRaw(t, conn, map[string]any{
		"handle": handle, "relPath": "private.json", "envRelPath": "environments/dev.json",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}
	if reached.Load() != 1 {
		t.Fatalf("the server was reached %d times, want 1", reached.Load())
	}
	if got, _ := gotAuth.Load().(string); got != "Bearer "+literal {
		t.Errorf("the server received Authorization %q, want the literal %q", got, "Bearer "+literal)
	}

	var got apiSendResponse
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal send result: %v", err)
	}
	// The literal is NOT elided: the frame carries it, and the rendered raw
	// request shows it. Nothing here rewrites, refuses or hides it.
	if !strings.Contains(string(frame), literal) {
		t.Errorf("the literal never reached the renderer: the raw diagnostic elided a value it must show")
	}
	if !strings.Contains(got.Request.Text, literal) {
		t.Errorf("the raw request does not show the literal the person typed:\n%s", got.Request.Text)
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
	root := apiAuthCollection(t, srv.URL, "{{token}}")

	handle := openAPICollection(t, conn, root, 1)
	resp, _ := sendRaw(t, conn, map[string]any{
		"handle": handle, "relPath": "private.json", "envRelPath": "environments/dev.json",
	}, 2)

	got := unresolvedRun(t, resp)
	if !strings.Contains(got.Failure.Reason, "token") {
		t.Errorf("the refusal reads %q and does not name the variable the person has to bind",
			got.Failure.Reason)
	}
	if strings.Contains(got.Failure.Reason, "cannot resolve") {
		t.Errorf("the refusal reads %q: that is the sender saying auth was never resolved, "+
			"not this environment saying the variable is unbound", got.Failure.Reason)
	}
	// The run says which environment it was asked of, which is half of
	// "where do I go to fix this".
	if got.Environment != "dev" {
		t.Errorf("environment = %q, want dev", got.Environment)
	}
	if reached.Load() != 0 {
		t.Errorf("the server was reached %d times, want 0 — no empty credential goes out", reached.Load())
	}
	if len(vlt.values) != 0 {
		t.Errorf("the vault holds %d values after a send that resolved nothing", len(vlt.values))
	}
}

// ── 3. a vault identifier in the file is a literal, and it is SENT ───────

// The file says `"auth":{"kind":"bearer","token":"keychain:nocx-secret-a"}`
// — a REAL identifier, minted by this test's own vault for the value the
// same person bound under `token`. Since auth is TEXT, the identifier is a
// literal: the send path has no syntax through which a file could name the
// bound value, so the request goes out with the identifier itself as the
// credential — the sentence "the auth variable is not bound in this
// environment" is gone, and nothing inspects the file's content.
//
// The bound path runs in the SAME test, against the same store and the
// same server, so the literal-is-sent claim is evidence about identifiers
// and not about a store that resolves nothing.
func TestAPIRequestSend_AVaultIdentifierInTheFileIsSentAsText(t *testing.T) {
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
	honest := apiAuthCollection(t, srv.URL, "{{token}}")
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
	if resp.Error != nil {
		t.Fatalf("the identifier request was refused, want it sent as the literal it is: %+v", resp.Error)
	}
	var hostileRun apiSendResponse
	if err := json.Unmarshal(resp.Result, &hostileRun); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := gotAuth.Load().(string); !strings.Contains(got, string(id)) {
		t.Errorf("the server received Authorization %q, want the identifier typed into the file", got)
	}
	if !strings.Contains(string(frame), string(id)) {
		t.Errorf("the literal identifier was elided from the frame: %s", frame)
	}
	// The bound VALUE never crossed as part of this: the file could not
	// reach it, which is the whole of §8.
	if strings.Contains(string(frame), "the reader's own token") {
		t.Errorf("the refusal frame carries the value the identifier points at: %s", frame)
	}

	// And the ordinary bound path, same store, same server: the literal
	// claim above is about identifiers, not about a world in which nothing
	// resolves.
	honestHandle := openAPICollection(t, conn, honest, 3)
	ok, _ := sendRaw(t, conn, map[string]any{
		"handle": honestHandle, "relPath": "private.json", "envRelPath": "environments/dev.json",
	}, 4)
	if ok.Error != nil {
		t.Fatalf("the ordinary bound variable failed too: %+v", ok.Error)
	}
	var okRun apiSendResponse
	if err := json.Unmarshal(ok.Result, &okRun); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := gotAuth.Load().(string); got != "Bearer the reader's own token" {
		t.Errorf("the server received Authorization %q, want the bound value", got)
	}
	if !strings.Contains(okRun.Request.Text, "⟦token⟧") {
		t.Errorf("the bound value's place is not badged: %s", okRun.Request.Text)
	}
}
