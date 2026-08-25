package transport

// A request's own variables, over the real socket (nocx-kprt4.1, .2).
//
// The file carries them, the wire carries them, and the send resolves them
// in front of the environment's. Everything here goes through the methods a
// person's panel calls — read, write, send — because the claim is about what
// crosses, and a struct test cannot make it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// received2 records the path one request arrived at.
type received2 struct {
	mu   sync.Mutex
	path string
	hits int
}

func (r *received2) get() (string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.path, r.hits
}

func varServer(t *testing.T) (*httptest.Server, *received2) {
	t.Helper()
	got := &received2{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.mu.Lock()
		got.path = r.URL.Path
		got.hits++
		got.mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// varCollection writes a request whose address has a hole the REQUEST fills,
// beside an environment that answers the rest — and that answers `id` too,
// so the order is observable rather than assumed.
func varCollection(t *testing.T, baseURL string) string {
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
	write("one.json", `{"id":"r1","name":"one","method":"GET","url":"{{baseUrl}}/users/{{id}}",`+
		`"headers":[],"query":[],"variables":[{"name":"id","value":"42","enabled":true}],`+
		`"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	// A SECOND request with a different value for the same name — the whole
	// reason the scope exists: an environment carrying both would be a place
	// to keep other people's values.
	write("two.json", `{"id":"r2","name":"two","method":"GET","url":"{{baseUrl}}/users/{{id}}",`+
		`"headers":[],"query":[],"variables":[{"name":"id","value":"99","enabled":true}],`+
		`"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("environments/dev.json", `{"name":"dev","values":{"baseUrl":`+mustJSON(t, baseURL)+
		`,"id":"the-environment-s"},"route":{"kind":"direct"}}`)
	return root
}

// THE FILE CARRIES THEM AND THE WIRE READS THEM BACK — the round trip a
// panel makes: read what is on disk, write it back edited, read it again.
func TestAPIRequest_VariablesSurviveTheReadWriteRoundTrip(t *testing.T) {
	srv, _ := varServer(t)
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := varCollection(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	read := vaultCall(t, conn, "api.request.read",
		map[string]any{"handle": handle, "relPath": "one.json"}, 2)
	if read.Error != nil {
		t.Fatalf("api.request.read: %+v", read.Error)
	}
	var got apiRequestReadResponse
	if err := json.Unmarshal(read.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Request.Variables) != 1 || got.Request.Variables[0].Name != "id" {
		t.Fatalf("variables = %+v, want the row the file declares", got.Request.Variables)
	}
	if got.Request.Variables[0].Value != "42" {
		t.Errorf("value = %q, want 42", got.Request.Variables[0].Value)
	}

	// Edited and written back the way the panel writes it.
	edited := got.Request
	edited.Variables = append(edited.Variables, apiParamWire{Name: "page", Value: "2", Enabled: true})
	if resp := vaultCall(t, conn, "api.request.write", map[string]any{
		"handle": handle, "relPath": "one.json", "request": edited,
	}, 3); resp.Error != nil {
		t.Fatalf("api.request.write: %+v", resp.Error)
	}

	back := vaultCall(t, conn, "api.request.read",
		map[string]any{"handle": handle, "relPath": "one.json"}, 4)
	if back.Error != nil {
		t.Fatalf("api.request.read (again): %+v", back.Error)
	}
	var reread apiRequestReadResponse
	if err := json.Unmarshal(back.Result, &reread); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(reread.Request.Variables) != 2 {
		t.Fatalf("variables = %+v, want both rows back", reread.Request.Variables)
	}
	if reread.Request.Variables[1].Name != "page" || reread.Request.Variables[1].Value != "2" {
		t.Errorf("second row = %+v, want the one just added", reread.Request.Variables[1])
	}

	// AND THE FILE ITSELF holds them, because the file is the truth (§6.4).
	onDisk, err := os.ReadFile(filepath.Join(root, "one.json")) //nolint:gosec // a path this test wrote
	if err != nil {
		t.Fatalf("read the file: %v", err)
	}
	if !strings.Contains(string(onDisk), `"variables"`) {
		t.Errorf("the file does not carry the variables: %s", onDisk)
	}
}

// A REQUEST WITH NO VARIABLES answers [] and never null — the renderer's
// first .map on a null throws, and the file is allowed to omit the key.
func TestAPIRequest_ARequestWithNoVariablesAnswersAnEmptyList(t *testing.T) {
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollection(t, conn, root, 1)

	read := vaultCall(t, conn, "api.request.read",
		map[string]any{"handle": handle, "relPath": "ping.json"}, 2)
	if read.Error != nil {
		t.Fatalf("api.request.read: %+v", read.Error)
	}
	// On the RAW frame, because a decoded nil and a decoded [] are the same
	// Go value and this is a claim about what crossed.
	var probe struct {
		Request struct {
			Variables json.RawMessage `json:"variables"`
		} `json:"request"`
	}
	if err := json.Unmarshal(read.Result, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(probe.Request.Variables) != "[]" {
		t.Errorf("variables = %s, want []", probe.Request.Variables)
	}
}

// THE ORDER, over the socket: two requests with different values for one
// name, under an environment that answers that name too. Each gets its own,
// and the environment's answer is what neither gets.
func TestAPIRequestSend_TheRequestsOwnVariableWinsOverTheEnvironments(t *testing.T) {
	srv, got := varServer(t)
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := varCollection(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	for rel, want := range map[string]string{"one.json": "/users/42", "two.json": "/users/99"} {
		resp := vaultCall(t, conn, "api.request.send", map[string]any{
			"handle": handle, "relPath": rel,
			"envRelPath": "environments/dev.json", "token": "t-" + rel,
		}, 2)
		if resp.Error != nil {
			t.Fatalf("api.request.send %s: %+v", rel, resp.Error)
		}
		path, _ := got.get()
		if path != want {
			t.Errorf("%s reached %q, want %q — the request's own variable did not win", rel, path, want)
		}
		if strings.Contains(path, "the-environment-s") {
			t.Errorf("%s reached %q — the environment answered a name the request answers", rel, path)
		}
	}

	// AND THE ENVIRONMENT IS STILL INHERITED: baseUrl came from it, or
	// nothing above would have reached a server at all.
	if _, hits := got.get(); hits != 2 {
		t.Errorf("the server was reached %d times, want 2", hits)
	}
}

// FOLDER VARIABLES sit between request and environment, and the nearest
// folder wins over its parent. The same real socket call also carries the
// presence list the tree needs, so no folder gets a second read round trip.
func TestAPIRequestSend_FolderVariablesResolveInNearestOrderAndListPresence(t *testing.T) {
	srv, got := varServer(t)
	_, conn := newAPIWSServer(t, newAPIFakeBindings())
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
	write(".variables.json", `{"variables":[{"name":"id","value":"root","enabled":true}]}`)
	write("users/.variables.json", `{"variables":[{"name":"id","value":"users","enabled":true}]}`)
	write("users/private/.variables.json", `{"variables":[{"name":"id","value":"private","enabled":true}]}`)
	write("users/own.json", `{"id":"own","name":"own","method":"GET","url":"{{baseUrl}}/users/{{id}}","variables":[{"name":"id","value":"request","enabled":true}],"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("users/inherited.json", `{"id":"inherited","name":"inherited","method":"GET","url":"{{baseUrl}}/users/{{id}}","body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("users/private/nested.json", `{"id":"nested","name":"nested","method":"GET","url":"{{baseUrl}}/users/{{id}}","body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("environments/dev.json", `{"name":"dev","values":{"baseUrl":`+mustJSON(t, srv.URL)+`,"id":"environment"},"route":{"kind":"direct"}}`)

	opened := vaultCall(t, conn, "api.collections.open", map[string]any{"path": root}, 1)
	if opened.Error != nil {
		t.Fatalf("api.collections.open: %+v", opened.Error)
	}
	var open apiOpenResponse
	if err := json.Unmarshal(opened.Result, &open); err != nil {
		t.Fatalf("decode open: %v", err)
	}
	handle := open.Handle

	listed := vaultCall(t, conn, "api.collections.list", map[string]any{}, 2)
	if listed.Error != nil {
		t.Fatalf("api.collections.list: %+v", listed.Error)
	}
	var list apiCollectionsListResponse
	if err := json.Unmarshal(listed.Result, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	var listedFolder *apiCollectionWire
	for i := range list.Collections {
		if list.Collections[i].Path == root {
			listedFolder = &list.Collections[i].Collection
			break
		}
	}
	if listedFolder == nil {
		t.Fatalf("api.collections.list did not return %q", root)
	}
	if !reflect.DeepEqual(listedFolder.VariableFolders, []string{"", "users", "users/private"}) {
		t.Fatalf("variableFolders = %v, want root and nearest folders", listedFolder.VariableFolders)
	}
	for rel, want := range map[string]string{
		"users/own.json":            "/users/request",
		"users/inherited.json":      "/users/users",
		"users/private/nested.json": "/users/private",
	} {
		resp := vaultCall(t, conn, "api.request.send", map[string]any{
			"handle": handle, "relPath": rel,
			"envRelPath": "environments/dev.json", "token": "folder-" + rel,
		}, 2)
		if resp.Error != nil {
			t.Fatalf("api.request.send %s: %+v", rel, resp.Error)
		}
		path, _ := got.get()
		if path != want {
			t.Fatalf("%s reached %q, want %q", rel, path, want)
		}
	}
	if _, hits := got.get(); hits != 3 {
		t.Fatalf("the server was reached %d times, want 3", hits)
	}
}

// THE REFUSAL, over the socket: a request row that would shadow a name the
// environment declares secret. It comes back as a RUN at compose — a thing
// that happened to somebody who pressed Send — naming the variable and never
// the row's value.
func TestAPIRequestSend_ARequestVariableShadowingASecretIsRefused(t *testing.T) {
	srv, got := varServer(t)
	_, conn := newAPIWSServer(t, newAPIFakeBindings())

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
		`"headers":[],"query":[],`+
		`"variables":[{"name":"token","value":"the-file-s-own-value","enabled":true}],`+
		`"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("environments/dev.json", `{"name":"dev","values":{"baseUrl":`+mustJSON(t, srv.URL)+
		`},"secretVars":["token"],"route":{"kind":"direct"}}`)

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
	if run.Outcome != "failed" {
		t.Fatalf("outcome = %q, want failed", run.Outcome)
	}
	if run.Failure == nil || run.Failure.Phase != "compose" {
		t.Fatalf("failure = %+v, want phase compose", run.Failure)
	}
	if !strings.Contains(run.Failure.Reason, "token") {
		t.Errorf("reason = %q, want it to name the variable", run.Failure.Reason)
	}
	if !strings.Contains(run.Failure.Reason, "secret") {
		t.Errorf("reason = %q, want it to say why", run.Failure.Reason)
	}
	// NEVER THE ROW'S VALUE. The reason crosses to the renderer and reaches
	// any log that prints it.
	if strings.Contains(run.Failure.Reason, "the-file-s-own-value") {
		t.Fatalf("the refusal carries the row's value: %s", run.Failure.Reason)
	}
	if _, hits := got.get(); hits != 0 {
		t.Errorf("the server was reached %d times by a refused send", hits)
	}
}
