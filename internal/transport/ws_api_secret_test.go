package transport

// A secret ANYWHERE in a request (epic nocx-ew3uv, beads .1 and .2).
//
// A vault-held value reached exactly one field — the auth variable, resolved
// by the sender — so a token that belongs in a PATH could only be sent by
// typing it into a file that goes into git. Telegram is the shape that names
// the gap: `/bot<TOKEN>/sendMessage`.
//
// Two halves, and both are here because neither is worth anything alone: the
// panel can now MINT a binding (api.environment.bindSecret), and the send
// RESOLVES one wherever text is substituted (apicoll.Chain's second lookup,
// which the snapshot never built).
//
// EVERYTHING RUNS OVER THE REAL SOCKET WITH THE REAL BINDING STORE
// (apibind.JSONStore over a real document store and a fake vault, the
// arrangement ws_api_auth_test.go established). A fake store would prove the
// handler calls whatever it was handed; the real one proves the value a
// person bound is the value the server receives.
//
// AND EVERY CASE ASSERTS THE ABSENCE. The server sees the real token — a
// test whose server did not check that would pass on a request carrying the
// literal `{{token}}` — and the value appears in no frame the renderer
// received, no file under the collection root, and no failure reason. That
// last one is not hypothetical: it was a real leak in this package's
// redaction, measured before the field existed (redact, apisend).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/apibind"
)

// THE VALUE, in one place, so every assertion below can say "this exact
// string appeared nowhere it should not".
// It is deliberately credential-SHAPED: every assertion below is that
// this exact string appeared nowhere it should not, and a fixture that
// looked less like a token would be a weaker test of the same property.
//
//nolint:gosec // a test fixture whose whole job is to be searched for
const secretValue = "sk-live-9f2c4e7a11b3d8-telegram"

// secretCollection writes a collection whose request uses `{{token}}` in
// EVERY field a variable can reach: the path, the query, a header and the
// body. One request, four places — a substitution that works in three out of
// four is the shape that ships.
func secretCollection(t *testing.T, baseURL string) string {
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
	// THE TELEGRAM SHAPE: the token is a PATH SEGMENT, which is the case no
	// header rule can cover — a value in a path cannot be stripped, it IS
	// the target.
	write("send.json", `{"id":"r1","name":"send","method":"POST",`+
		`"url":"{{baseUrl}}/bot{{token}}/sendMessage",`+
		`"headers":[{"name":"X-Probe","value":"h-{{token}}","enabled":true}],`+
		`"query":[{"name":"q","value":"q-{{token}}","enabled":true}],`+
		`"body":{"kind":"raw","text":"b-{{token}}"},"auth":{"kind":"bearer","token":"{{token}}"}}`)
	// The file declares the NAME and never the value — there is no field in
	// this format one could be typed into (§8).
	write("environments/dev.json", `{"name":"dev","values":{"baseUrl":`+mustJSON(t, baseURL)+`},`+
		`"secretVars":["token"],"route":{"kind":"direct"}}`)
	return root
}

// received is what the server actually got, so a test can assert the REAL
// value arrived rather than the literal reference.
type received struct {
	mu    sync.Mutex
	path  string
	query string
	head  string
	auth  string
	body  string
	hits  int
}

func (r *received) record(req *http.Request, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits++
	r.path = req.URL.Path
	r.query = req.URL.RawQuery
	r.head = req.Header.Get("X-Probe")
	r.auth = req.Header.Get("Authorization")
	r.body = body
}

func (r *received) snapshot() received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return received{path: r.path, query: r.query, head: r.head, auth: r.auth, body: r.body, hits: r.hits}
}

// secretServer answers anything and remembers what it was sent.
func secretServer(t *testing.T) (*httptest.Server, *received) {
	t.Helper()
	got := &received{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		got.record(r, string(buf[:n]))
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// bindThroughTheProduct gives the variable its value the way a person will:
// the method, over the socket. It is not seeded into the store — that would
// prove the send resolves whatever a test put there and say nothing about
// whether anybody can put it there.
func bindThroughTheProduct(t *testing.T, conn *websocket.Conn, handle, variable, value string, id int) {
	t.Helper()
	resp := vaultCall(t, conn, "api.environment.bindSecret", map[string]any{
		"handle": handle, "relPath": "environments/dev.json",
		"variable": variable, "value": value,
	}, id)
	if resp.Error != nil {
		t.Fatalf("api.environment.bindSecret: %+v", resp.Error)
	}
	// NOTHING COMES BACK, checked here rather than in one test, so every
	// case in this file pays for the assertion: the value went one way, and
	// neither it nor an identifier for it is in what the method answers.
	if strings.Contains(string(resp.Result), value) {
		t.Fatalf("the bind echoed the value: %s", resp.Result)
	}
	if got := strings.TrimSpace(string(resp.Result)); got != "{}" {
		t.Errorf("result = %s, want the empty object", got)
	}
}

// ── .1 the panel can mint a secret variable ───────────────────────────────

// THE VALUE LANDS IN THE VAULT, UNDER THE KEY THE READ HALF USES, and
// nothing about it lands in the collection folder.
func TestAPIEnvironmentBindSecret_PutsTheValueInTheVaultAndNothingInTheFolder(t *testing.T) {
	srv, _ := secretServer(t)
	bindings, vlt := apiAuthStore(t)
	_, conn := newAPIWSServer(t, bindings)
	root := secretCollection(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	bindThroughTheProduct(t, conn, handle, "token", secretValue, 2)

	// KEYED BY THE COLLECTION AND THE ENVIRONMENT'S NAME — the pair the send
	// path resolves against. A binding written under any other key is a
	// binding nothing can read, which is the defect a second derivation of
	// the key would produce.
	id, found, err := bindings.Lookup(apibind.Key{
		Collection: root, Environment: "dev", Variable: "token",
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !found {
		t.Fatal("nothing was bound under the key the send path reads")
	}
	if id == "" {
		t.Error("the binding names no stored value")
	}

	// THE VAULT HOLDS IT.
	vlt.mu.Lock()
	stored := len(vlt.values)
	vlt.mu.Unlock()
	if stored != 1 {
		t.Errorf("the vault holds %d values, want the one just bound", stored)
	}

	// AND THE FOLDER HOLDS NO BYTE OF IT. A walk, not a look at one file:
	// the claim is that a collection folder is safe to commit BY
	// CONSTRUCTION (§8), so the thing to check is the whole folder.
	for _, file := range walkFiles(t, root) {
		text, readErr := os.ReadFile(file) //nolint:gosec // a path this test wrote
		if readErr != nil {
			t.Fatalf("read %s: %v", file, readErr)
		}
		if strings.Contains(string(text), secretValue) {
			t.Errorf("%s carries the value", file)
		}
		if strings.Contains(string(text), id) {
			t.Errorf("%s names the stored value's identifier", file)
		}
	}
	// The file DOES declare the name, or the absence above would pass on a
	// build that dropped the whole thing on the floor.
	env, err := os.ReadFile(filepath.Join(root, "environments", "dev.json")) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatalf("read the environment: %v", err)
	}
	if !strings.Contains(string(env), `"token"`) {
		t.Errorf("the environment file no longer declares the variable: %s", env)
	}
}

// The method REFUSES what it cannot key, and none of its refusals says the
// value. This is the one params object on the api surface that carries a
// credential inbound, so what its errors contain is part of the feature.
func TestAPIEnvironmentBindSecret_RefusalsNameTheVariableAndNeverTheValue(t *testing.T) {
	srv, _ := secretServer(t)
	bindings, _ := apiAuthStore(t)
	_, conn := newAPIWSServer(t, bindings)
	root := secretCollection(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	id := 1
	for name, params := range map[string]map[string]any{
		"an environment that is not there": {
			"handle": handle, "relPath": "environments/nope.json",
			"variable": "token", "value": secretValue,
		},
		"a path that leaves the collection": {
			"handle": handle, "relPath": "../../etc/passwd",
			"variable": "token", "value": secretValue,
		},
		"a handle nobody minted": {
			"handle": "0123456789abcdef0123456789abcdef", "relPath": "environments/dev.json",
			"variable": "token", "value": secretValue,
		},
		"no variable named": {
			"handle": handle, "relPath": "environments/dev.json",
			"variable": "", "value": secretValue,
		},
		"no value at all": {
			"handle": handle, "relPath": "environments/dev.json",
			"variable": "token", "value": "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			id++
			resp := vaultCall(t, conn, "api.environment.bindSecret", params, id)
			if resp.Error == nil {
				t.Fatalf("accepted %+v", params)
			}
			if strings.Contains(resp.Error.Message, secretValue) {
				t.Errorf("the refusal carries the value: %s", resp.Error.Message)
			}
		})
	}
}

// ── .2 a secret resolves wherever text is sent ────────────────────────────

// ONE TEST PER FIELD, and one server that says what it received. Four
// places, because a substitution that works in three of them is the shape
// that ships — and the ASSERTION IS THE SERVER'S: without it this would pass
// on a request that carried the literal `{{token}}` to a server that did not
// care.
func TestAPIRequestSend_ASecretResolvesInEveryFieldTextIsSubstitutedInto(t *testing.T) {
	srv, got := secretServer(t)
	bindings, _ := apiAuthStore(t)
	_, conn := newAPIWSServer(t, bindings)
	root := secretCollection(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)
	bindThroughTheProduct(t, conn, handle, "token", secretValue, 2)

	resp, frame := sendRaw(t, conn, map[string]any{
		"handle": handle, "relPath": "send.json",
		"envRelPath": "environments/dev.json",
	}, 3)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}

	arrived := got.snapshot()
	if arrived.hits != 1 {
		t.Fatalf("the server was reached %d times, want 1", arrived.hits)
	}
	for _, field := range []struct{ what, saw string }{
		{"the path", arrived.path},
		{"the query", arrived.query},
		{"the header", arrived.head},
		{"the body", arrived.body},
		{"the auth header", arrived.auth},
	} {
		if !strings.Contains(field.saw, secretValue) {
			t.Errorf("%s arrived as %q — the variable was not resolved into it", field.what, field.saw)
		}
		if strings.Contains(field.saw, "{{token}}") {
			t.Errorf("%s arrived as %q — the reference crossed instead of the value", field.what, field.saw)
		}
	}

	// AND NO BYTE OF IT CAME BACK. The frame is the whole response as it
	// reached the renderer, which is where a diagnostic that showed what it
	// sent would put it.
	if strings.Contains(string(frame), secretValue) {
		t.Errorf("the value crossed to the renderer in the send frame: %s", frame)
	}
	// …because it was ELIDED rather than absent: the raw request shows the
	// placeholder in all five places, which is what makes the assertion
	// above about redaction rather than about an empty diagnostic.
	var run apiSendResponse
	if err := json.Unmarshal(resp.Result, &run); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n := strings.Count(run.Request.Text, "⟦token⟧"); n != 5 {
		t.Errorf("the raw request marks the secret %d times, want 5 (path, query, header, body, auth):\n%s",
			n, run.Request.Text)
	}
}

// A VARIABLE THE BINDING DOCUMENT DOES NOT ANSWER blocks the send and names
// itself — the same refusal an unbound plain variable gets (§6.5). Without
// this, "the secret resolved" could be true of a build that substituted an
// empty string.
func TestAPIRequestSend_ASecretVariableNobodyBoundBlocksTheSend(t *testing.T) {
	srv, got := secretServer(t)
	bindings, _ := apiAuthStore(t)
	_, conn := newAPIWSServer(t, bindings)
	root := secretCollection(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)
	// Nothing is bound.

	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "send.json",
		"envRelPath": "environments/dev.json", "token": "t-1",
	}, 3)
	if resp.Error != nil {
		t.Fatalf("expected a run, got an error: %+v", resp.Error)
	}
	var run apiSendResponse
	if err := json.Unmarshal(resp.Result, &run); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if run.Failure == nil || run.Failure.Phase != "compose" {
		t.Fatalf("failure = %+v, want phase compose", run.Failure)
	}
	if !strings.Contains(run.Failure.Reason, "token") {
		t.Errorf("reason = %q, want it to name the variable that has no value", run.Failure.Reason)
	}
	if arrived := got.snapshot(); arrived.hits != 0 {
		t.Errorf("the server was reached %d times by a request with an unresolved variable", arrived.hits)
	}
}

// THE ORDER OF THE CHAIN, which is §8 stated as behaviour: a collection
// arriving in a pull request must not be able to choose what a reader's
// request sends. The file declares `token` secret, so its own plain value
// for that name is refused by Environment.Value and the binding answers —
// the file cannot shadow the vault, and it cannot reach the vault either.
func TestAPIRequestSend_AFileCannotShadowABoundSecret(t *testing.T) {
	srv, got := secretServer(t)
	bindings, _ := apiAuthStore(t)
	_, conn := newAPIWSServer(t, bindings)

	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("nocx-collection.json", `{"schemaVersion":1,"name":"acme"}`)
	write("send.json", `{"id":"r1","name":"send","method":"GET","url":"{{baseUrl}}/bot{{token}}/x",`+
		`"headers":[],"query":[],"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	// The hostile half: the file declares token secret AND writes a value
	// for it. The declaration wins; the plain value is dead text.
	write("environments/dev.json", `{"name":"dev","values":{"baseUrl":`+mustJSON(t, srv.URL)+
		`,"token":"the-file-s-own-value"},"secretVars":["token"],"route":{"kind":"direct"}}`)

	handle := openAPICollection(t, conn, root, 1)
	bindThroughTheProduct(t, conn, handle, "token", secretValue, 2)

	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "send.json",
		"envRelPath": "environments/dev.json", "token": "t-1",
	}, 3)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}

	arrived := got.snapshot()
	if !strings.Contains(arrived.path, secretValue) {
		t.Errorf("path = %q, want the value the READER bound", arrived.path)
	}
	if strings.Contains(arrived.path, "the-file-s-own-value") {
		t.Errorf("path = %q — the collection file chose what was sent", arrived.path)
	}
}

// A BUILD WITH NO BINDING STORE resolves no secret variable, and says so the
// way every unresolved variable is said. The half that keeps "it resolved"
// from being a property of the test's wiring.
func TestAPIRequestSend_WithNoBindingStoreASecretVariableIsSimplyUnresolved(t *testing.T) {
	srv, got := secretServer(t)
	// newAPIFakeBindings implements the write half and NOT the resolver, so
	// this is a build that can import and cannot resolve (newAPIWSServer).
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := secretCollection(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "send.json",
		"envRelPath": "environments/dev.json", "token": "t-1",
	}, 2)
	if resp.Error != nil {
		t.Fatalf("expected a run, got an error: %+v", resp.Error)
	}
	var run apiSendResponse
	if err := json.Unmarshal(resp.Result, &run); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if run.Failure == nil || !strings.Contains(run.Failure.Reason, "token") {
		t.Errorf("failure = %+v, want the unresolved variable named", run.Failure)
	}
	if arrived := got.snapshot(); arrived.hits != 0 {
		t.Errorf("the server was reached %d times with nothing bound", arrived.hits)
	}
}

// walkFiles lists every file under root, recursively.
func walkFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			out = append(out, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}
