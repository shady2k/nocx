package transport

// What the api.* handlers do that no other layer can be asked about: the
// ENVIRONMENT the caller names reaches the sender as a route and an address
// TOGETHER (design §6.5), and a collection can be made from nothing.
//
// The sender here is a fake that records the Key it was handed. That is the
// seam under test: apisend's own tests prove a connection route dials
// through a lease, and these prove the route that reaches it is the one the
// environment declared rather than the direct one the handler used to pass
// unconditionally.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apisend"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

// recordingSender is an apisend.Sender that answers nothing and remembers
// what it was asked to send.
type recordingSender struct {
	mu   sync.Mutex
	keys []apisend.Key
	reqs []apicoll.Request
	err  error
}

func (s *recordingSender) Send(_ context.Context, r apicoll.Request, k apisend.Key, _ ...apisend.NamedSecret) (apisend.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, k)
	s.reqs = append(s.reqs, r)
	if s.err != nil {
		return apisend.Response{}, s.err
	}
	return apisend.Response{Status: 204, Headers: []apicoll.Header{}}, nil
}

func (s *recordingSender) last(t *testing.T) (apisend.Key, apicoll.Request) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.keys) == 0 {
		t.Fatal("the sender was never asked to send anything")
	}
	return s.keys[len(s.keys)-1], s.reqs[len(s.reqs)-1]
}

func (s *recordingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}

// newAPIWSServerWithSender builds a server whose sender is the caller's.
func newAPIWSServerWithSender(t *testing.T, sender apisend.Sender) *websocket.Conn {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger),
		WithAPI(apicoll.NewCollections(apiTestPaths(t)), sender))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return connectWS(t, ws)
}

// apiEnvironmentFolder writes a collection whose request is written in
// variables and whose environments answer WHERE and HOW in one record.
func apiEnvironmentFolder(t *testing.T) string {
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
	write("users.json", `{"id":"r1","name":"users","method":"GET","url":"{{baseUrl}}/users",`+
		`"body":{"kind":"none"},"auth":{"kind":"none"}}`)
	write("environments/dev.json",
		`{"name":"dev","values":{"baseUrl":"http://localhost:3000"},"route":{"kind":"direct"}}`)
	write("environments/prod.json",
		`{"name":"prod","values":{"baseUrl":"https://api.internal"},"route":{"kind":"connection","profileId":"ssh:bastion:1"}}`)
	write("environments/nobase.json", `{"name":"nobase","values":{},"route":{"kind":"direct"}}`)
	return root
}

// The property the whole route design exists for, at the layer where it
// could still be lost: SWITCHING ENVIRONMENT MOVES THE ADDRESS AND THE
// ROUTE TOGETHER. One request, two environments, and the sender sees both
// facts change at once.
func TestAPIRequestSend_TheEnvironmentsRouteAndAddressReachTheSender(t *testing.T) {
	sender := &recordingSender{}
	conn := newAPIWSServerWithSender(t, sender)
	root := apiEnvironmentFolder(t)
	handle := openAPICollection(t, conn, root, 1)

	// prod: through the bastion, at the internal address.
	resp := vaultCall(t, conn, "api.request.send",
		map[string]any{"handle": handle, "relPath": "users.json", "envRelPath": "environments/prod.json"}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send under prod: %+v", resp.Error)
	}
	key, req := sender.last(t)
	wantID, err := apisend.RouteIDFor(apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "ssh:bastion:1"})
	if err != nil {
		t.Fatalf("RouteIDFor: %v", err)
	}
	if key.RouteID != wantID {
		t.Errorf("RouteID = %q, want %q — the environment's route did not reach the sender", key.RouteID, wantID)
	}
	if req.URL != "https://api.internal/users" {
		t.Errorf("URL = %q, want the prod address", req.URL)
	}
	if key.CookieScope != root {
		t.Errorf("CookieScope = %q, want the collection %q", key.CookieScope, root)
	}

	// dev: out of this machine, at the local address. Same request file.
	resp = vaultCall(t, conn, "api.request.send",
		map[string]any{"handle": handle, "relPath": "users.json", "envRelPath": "environments/dev.json"}, 3)
	if resp.Error != nil {
		t.Fatalf("api.request.send under dev: %+v", resp.Error)
	}
	key, req = sender.last(t)
	if key.RouteID != "" {
		t.Errorf("RouteID = %q, want the direct route", key.RouteID)
	}
	if req.URL != "http://localhost:3000/users" {
		t.Errorf("URL = %q, want the dev address", req.URL)
	}
}

// No environment named is the direct route and the request as written: the
// pane sends before anybody has configured anything.
func TestAPIRequestSend_WithNoEnvironmentIsStillTheDirectRoute(t *testing.T) {
	sender := &recordingSender{}
	conn := newAPIWSServerWithSender(t, sender)
	root := apiCollectionFolder(t, "https://example.test/ping")
	handle := openAPICollection(t, conn, root, 1)

	resp := vaultCall(t, conn, "api.request.send",
		map[string]any{"handle": handle, "relPath": "ping.json"}, 2)
	if resp.Error != nil {
		t.Fatalf("api.request.send: %+v", resp.Error)
	}
	key, req := sender.last(t)
	if key.RouteID != "" {
		t.Errorf("RouteID = %q, want the direct route", key.RouteID)
	}
	if req.URL != "https://example.test/ping" {
		t.Errorf("URL = %q, want the file's own", req.URL)
	}
}

// An unresolved variable BLOCKS the send and names itself. The assertion
// that matters is the second one: the sender was never asked, so there is no
// request going out with empty braces or an empty string in it.
func TestAPIRequestSend_AnUnresolvedVariableBlocksTheSendAndNamesItself(t *testing.T) {
	sender := &recordingSender{}
	conn := newAPIWSServerWithSender(t, sender)
	root := apiEnvironmentFolder(t)
	handle := openAPICollection(t, conn, root, 1)

	resp := vaultCall(t, conn, "api.request.send",
		map[string]any{"handle": handle, "relPath": "users.json", "envRelPath": "environments/nobase.json"}, 2)
	if resp.Error == nil {
		t.Fatal("api.request.send succeeded with an unresolved variable")
	}
	if !strings.Contains(resp.Error.Message, "baseUrl") {
		t.Errorf("message = %q, want it to name the variable that has no value", resp.Error.Message)
	}
	if got := sender.count(); got != 0 {
		t.Errorf("the sender was asked to send %d times, want 0 — a request went out unresolved", got)
	}
}

// An environment file that is not there, or a path that leaves the
// collection, is refused before anything is sent.
func TestAPIRequestSend_RefusesAnEnvironmentPathTheCollectionDoesNotOwn(t *testing.T) {
	sender := &recordingSender{}
	conn := newAPIWSServerWithSender(t, sender)
	root := apiEnvironmentFolder(t)
	handle := openAPICollection(t, conn, root, 1)

	id := 1
	for name, rel := range map[string]string{
		"escaping the folder": "../../etc/passwd",
		"not an environment":  "users.json",
		"not there at all":    "environments/nope.json",
	} {
		t.Run(name, func(t *testing.T) {
			id++
			resp := vaultCall(t, conn, "api.request.send",
				map[string]any{"handle": handle, "relPath": "users.json", "envRelPath": rel}, id)
			if resp.Error == nil {
				t.Fatalf("api.request.send accepted envRelPath %q", rel)
			}
		})
	}
	if got := sender.count(); got != 0 {
		t.Errorf("the sender was asked to send %d times, want 0", got)
	}
}

// ─── making one ────────────────────────────────────────────────────────────

// The whole motion a user performs when they have no collection at all: name
// one, and it is open, listed, and ready for a request. Before this the pane
// could open a folder and never make one.
func TestAPICollectionsCreate_MakesACollectionThatIsOpenAndUsable(t *testing.T) {
	conn := newAPIWSServerWithSender(t, &recordingSender{})

	resp := vaultCall(t, conn, "api.collections.create", map[string]any{"name": "acme"}, 1)
	if resp.Error != nil {
		t.Fatalf("api.collections.create: %+v", resp.Error)
	}
	var made apiOpenResponse
	if err := json.Unmarshal(resp.Result, &made); err != nil {
		t.Fatalf("unmarshal create result: %v", err)
	}
	if made.Handle == "" {
		t.Fatal("api.collections.create returned an empty handle")
	}
	if made.Collection.Name != "acme" {
		t.Errorf("name = %q, want acme", made.Collection.Name)
	}
	if made.Collection.Requests == nil || made.Collection.Malformed == nil {
		t.Errorf("collection = %+v, want [] rather than null for both lists", made.Collection)
	}

	// It is in the list the pane renders.
	listed := vaultCall(t, conn, "api.collections.list", map[string]any{}, 2)
	if listed.Error != nil {
		t.Fatalf("api.collections.list: %+v", listed.Error)
	}
	var list apiCollectionsListResponse
	if err := json.Unmarshal(listed.Result, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list.Collections) != 1 || list.Collections[0].Handle != made.Handle {
		t.Fatalf("opened folders = %+v, want the one just created", list.Collections)
	}
	if list.Collections[0].Error != "" {
		t.Errorf("the created folder reports %q; it must be readable", list.Collections[0].Error)
	}

	// And the handle is a working handle: the next thing the user does is
	// add a request, and it must not need an open first.
	write := vaultCall(t, conn, "api.request.write", map[string]any{
		"handle": made.Handle, "relPath": "ping.json",
		"request": map[string]any{
			"id": "r1", "name": "ping", "method": "GET", "url": "https://example.test",
			"body": map[string]any{"kind": "none"}, "auth": map[string]any{"kind": "none"},
		},
	}, 3)
	if write.Error != nil {
		t.Fatalf("api.request.write into the new collection: %+v", write.Error)
	}
	read := vaultCall(t, conn, "api.request.read",
		map[string]any{"handle": made.Handle, "relPath": "ping.json"}, 4)
	if read.Error != nil {
		t.Fatalf("api.request.read from the new collection: %+v", read.Error)
	}
}

// A name that is a path is refused, and refused as the CALLER's error: the
// remedy is to name something else.
func TestAPICollectionsCreate_RefusesANameThatIsNotAName(t *testing.T) {
	conn := newAPIWSServerWithSender(t, &recordingSender{})

	id := 10
	for name, given := range map[string]string{
		"empty":         "",
		"a slash":       "acme/prod",
		"the parent":    "..",
		"an absolute":   "/etc/acme",
		"a leading dot": ".hidden",
	} {
		t.Run(name, func(t *testing.T) {
			id++
			resp := vaultCall(t, conn, "api.collections.create", map[string]any{"name": given}, id)
			if resp.Error == nil {
				t.Fatalf("api.collections.create accepted %q", given)
			}
			if resp.Error.Code != -32602 {
				t.Errorf("code = %d, want -32602 — the caller's move is to name something else", resp.Error.Code)
			}
		})
	}
}

// The second create under a taken name is refused rather than merged, and
// the first collection's contents are still there afterwards.
func TestAPICollectionsCreate_RefusesToWriteOverAnExistingCollection(t *testing.T) {
	conn := newAPIWSServerWithSender(t, &recordingSender{})

	first := vaultCall(t, conn, "api.collections.create", map[string]any{"name": "acme"}, 1)
	if first.Error != nil {
		t.Fatalf("first create: %+v", first.Error)
	}
	var made apiOpenResponse
	if err := json.Unmarshal(first.Result, &made); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	write := vaultCall(t, conn, "api.request.write", map[string]any{
		"handle": made.Handle, "relPath": "keep.json",
		"request": map[string]any{
			"id": "r1", "name": "Keep", "method": "GET", "url": "https://example.test",
			"body": map[string]any{"kind": "none"}, "auth": map[string]any{"kind": "none"},
		},
	}, 2)
	if write.Error != nil {
		t.Fatalf("write: %+v", write.Error)
	}

	second := vaultCall(t, conn, "api.collections.create", map[string]any{"name": "acme"}, 3)
	if second.Error == nil {
		t.Fatal("the second create succeeded; a fresh manifest was written over an existing collection")
	}
	if second.Error.Code != -32602 {
		t.Errorf("code = %d, want -32602", second.Error.Code)
	}

	read := vaultCall(t, conn, "api.request.read",
		map[string]any{"handle": made.Handle, "relPath": "keep.json"}, 4)
	if read.Error != nil {
		t.Fatalf("the first collection's request is gone after the refused create: %+v", read.Error)
	}
}

// apiTestPaths is a storage.Paths whose three roles land under one test
// root: a created collection goes there rather than into the developer's own
// app directory.
type apiTestPathsT struct{ root string }

func (p apiTestPathsT) ConfigDir() string { return filepath.Join(p.root, "config") }
func (p apiTestPathsT) DataDir() string   { return filepath.Join(p.root, "data") }
func (p apiTestPathsT) CacheDir() string  { return filepath.Join(p.root, "cache") }

func apiTestPaths(t *testing.T) storage.Paths { return apiTestPathsT{root: t.TempDir()} }
