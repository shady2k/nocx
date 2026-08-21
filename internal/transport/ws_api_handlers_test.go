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
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

// The result SAYS which environment answered, and it says it in the name the
// FILE declares rather than in the path the caller named. That closes the
// loop the renderer cannot close on its own: a run list drawn from what the
// caller believed it asked for would be `vault.status.defaultProvider` in
// reverse — a value one side writes and the other never reads back.
//
// It is asserted off the socket for the same reason. Every case here is a
// real send through the real method, and the third one is the pair AGENTS.md
// asks for: for every "answers X when an environment is named" there is the
// one where none is.
func TestAPIRequestSend_TheResultNamesTheEnvironmentThatAnswered(t *testing.T) {
	sender := &recordingSender{}
	conn := newAPIWSServerWithSender(t, sender)
	root := apiEnvironmentFolder(t)
	handle := openAPICollection(t, conn, root, 1)

	environmentOf := func(t *testing.T, params map[string]any, id int) string {
		t.Helper()
		resp := vaultCall(t, conn, "api.request.send", params, id)
		if resp.Error != nil {
			t.Fatalf("api.request.send: %+v", resp.Error)
		}
		var got struct {
			Environment string `json:"environment"`
		}
		if err := json.Unmarshal(resp.Result, &got); err != nil {
			t.Fatalf("decode send: %v", err)
		}
		return got.Environment
	}

	if got := environmentOf(t, map[string]any{
		"handle": handle, "relPath": "users.json", "envRelPath": "environments/prod.json",
	}, 2); got != "prod" {
		t.Errorf("environment = %q, want %q — the name inside the file", got, "prod")
	}
	if got := environmentOf(t, map[string]any{
		"handle": handle, "relPath": "users.json", "envRelPath": "environments/dev.json",
	}, 3); got != "dev" {
		t.Errorf("environment = %q, want %q", got, "dev")
	}
	// No environment named: "" rather than a guess, which is the request as
	// written on the direct route and an ordinary state (§6.2).
	direct := apiCollectionFolder(t, "https://example.test/ping")
	directHandle := openAPICollection(t, conn, direct, 4)
	if got := environmentOf(t, map[string]any{
		"handle": directHandle, "relPath": "ping.json",
	}, 5); got != "" {
		t.Errorf("environment = %q, want \"\" — no environment was named", got)
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

// ─── the environments a person can choose between ──────────────────────────
//
// nocx-pnvnn. api.request.send has accepted `envRelPath` since it was
// written, and the renderer had no way to learn that any environment
// existed: apicoll.ListEnvironments had no caller anywhere outside apicoll's
// own tests, so every send went out with no environment and a collection
// whose URL is `{{baseUrl}}/…` — nearly every Postman export — failed from
// the product while working perfectly over the control plane. These are the
// wire half; the seam a person reaches is driven in
// frontend/src/api/api-workbench.test.tsx.

// The listing NAMES the environments, off the real socket. A test that
// validated a payload it built itself would prove the struct is well formed
// and say nothing about whether the server sends it — which is the defect
// this whole check exists for.
func TestAPICollections_TheWireNamesTheEnvironmentsAPersonCanChooseBetween(t *testing.T) {
	conn := newAPIWSServerWithSender(t, &recordingSender{})
	root := apiEnvironmentFolder(t)

	openResp := vaultCall(t, conn, "api.collections.open", map[string]any{"path": root}, 1)
	if openResp.Error != nil {
		t.Fatalf("api.collections.open: %+v", openResp.Error)
	}
	var opened struct {
		Handle     string `json:"handle"`
		Collection struct {
			Environments []struct {
				RelPath string `json:"relPath"`
				Name    string `json:"name"`
			} `json:"environments"`
		} `json:"collection"`
	}
	if err := json.Unmarshal(openResp.Result, &opened); err != nil {
		t.Fatalf("decode open: %v", err)
	}
	got := map[string]string{}
	for _, e := range opened.Collection.Environments {
		got[e.RelPath] = e.Name
	}
	want := map[string]string{
		"environments/dev.json":    "dev",
		"environments/nobase.json": "nobase",
		"environments/prod.json":   "prod",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environments = %+v, want %+v", got, want)
	}

	// And the SAME folder answers the same way through the listing, because
	// the panel re-lists after every change on disk and a picker that emptied
	// on the next refresh would be a control that governs nothing.
	listResp := vaultCall(t, conn, "api.collections.list", map[string]any{}, 2)
	if listResp.Error != nil {
		t.Fatalf("api.collections.list: %+v", listResp.Error)
	}
	var listed struct {
		Collections []struct {
			Handle     string `json:"handle"`
			Collection struct {
				Environments []struct {
					RelPath string `json:"relPath"`
					Name    string `json:"name"`
				} `json:"environments"`
			} `json:"collection"`
		} `json:"collections"`
	}
	if err := json.Unmarshal(listResp.Result, &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	// BY HANDLE, not by position. The listing also carries the built-in
	// collection, which every stand opens before it answers; this test is
	// about the folder it opened itself.
	found := false
	for _, c := range listed.Collections {
		if c.Handle != opened.Handle {
			continue
		}
		found = true
		if len(c.Collection.Environments) != 3 {
			t.Fatalf("listing carried %+v, want the same three environments", c.Collection.Environments)
		}
	}
	if !found {
		t.Fatalf("listing = %+v, want the folder that was opened in it", listed.Collections)
	}
}

// The environment's VALUES are not on the wire, and the assertion is on the
// serialised bytes rather than on a decoded struct: a field nobody names is
// a field whose presence nobody notices, and apicoll.EnvironmentRef embeds
// the whole Environment — values included — so marshalling the domain type
// by accident is one line away at all times.
func TestAPICollections_TheWireCarriesNoEnvironmentValues(t *testing.T) {
	conn := newAPIWSServerWithSender(t, &recordingSender{})
	root := apiEnvironmentFolder(t)

	resp := vaultCall(t, conn, "api.collections.open", map[string]any{"path": root}, 1)
	if resp.Error != nil {
		t.Fatalf("api.collections.open: %+v", resp.Error)
	}
	for _, leaked := range []string{"https://api.internal", "http://localhost:3000", "baseUrl", "ssh:bastion:1"} {
		if bytes.Contains(resp.Result, []byte(leaked)) {
			t.Errorf("the open result carries %q — an environment's contents reached the renderer", leaked)
		}
	}
}

// A brand-new collection answers [] rather than null. Both lists have always
// had to; this is the third, and a renderer's first .map on a null is a
// crash rather than an empty picker.
func TestAPICollectionsCreate_ANewCollectionHasAnEmptyEnvironmentList(t *testing.T) {
	conn := newAPIWSServerWithSender(t, &recordingSender{})

	resp := vaultCall(t, conn, "api.collections.create", map[string]any{"name": "acme"}, 1)
	if resp.Error != nil {
		t.Fatalf("api.collections.create: %+v", resp.Error)
	}
	if !bytes.Contains(resp.Result, []byte(`"environments":[]`)) {
		t.Fatalf("create answered %s, want an empty environments list", resp.Result)
	}
}

// A malformed environment file is NAMED beside the good ones rather than
// hiding them — apicoll's rule, carried through to the wire, and it lands in
// the collection's own malformed list because "a file in here that cannot be
// read" is one question.
func TestAPICollections_AMalformedEnvironmentIsNamedAndHidesNothing(t *testing.T) {
	conn := newAPIWSServerWithSender(t, &recordingSender{})
	root := apiEnvironmentFolder(t)
	if err := os.WriteFile(filepath.Join(root, "environments", "broken.json"), []byte(`{`), 0o600); err != nil {
		t.Fatalf("write a broken environment: %v", err)
	}

	resp := vaultCall(t, conn, "api.collections.open", map[string]any{"path": root}, 1)
	if resp.Error != nil {
		t.Fatalf("api.collections.open: %+v", resp.Error)
	}
	var opened struct {
		Collection struct {
			Malformed []struct {
				RelPath string `json:"relPath"`
				Reason  string `json:"reason"`
			} `json:"malformed"`
			Environments []struct {
				RelPath string `json:"relPath"`
			} `json:"environments"`
		} `json:"collection"`
	}
	if err := json.Unmarshal(resp.Result, &opened); err != nil {
		t.Fatalf("decode open: %v", err)
	}
	if len(opened.Collection.Environments) != 3 {
		t.Errorf("environments = %+v, want the three good ones", opened.Collection.Environments)
	}
	named := false
	for _, m := range opened.Collection.Malformed {
		if m.RelPath == "environments/broken.json" && m.Reason != "" {
			named = true
		}
	}
	if !named {
		t.Errorf("malformed = %+v, want the broken environment named with a reason", opened.Collection.Malformed)
	}
}

// The paired failure (AGENTS.md rule 3): the one external call this read
// makes is reading the directory, and when it fails the LISTING still
// answers — the reason lands on that one folder's error, where the panel
// renders it. A folder that quietly listed as having no environments would
// offer "None" as the whole truth and send {{baseUrl}} unresolved.
func TestAPICollectionsList_AnUnreadableEnvironmentsFolderIsOnTheEntryNotTheListing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny a read")
	}
	conn := newAPIWSServerWithSender(t, &recordingSender{})
	root := apiEnvironmentFolder(t)
	handle := openAPICollection(t, conn, root, 1)

	dir := filepath.Join(root, "environments")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	//nolint:gosec // G302: a DIRECTORY restored to the mode a collection folder uses; 0600 has no execute bit and would leave it unenterable for the cleanup
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	resp := vaultCall(t, conn, "api.collections.list", map[string]any{}, 2)
	if resp.Error != nil {
		t.Fatalf("api.collections.list refused the whole listing: %+v", resp.Error)
	}
	var listed struct {
		Collections []struct {
			Handle     string `json:"handle"`
			Error      string `json:"error"`
			Collection struct {
				Requests []struct {
					RelPath string `json:"relPath"`
				} `json:"requests"`
				Environments []struct {
					RelPath string `json:"relPath"`
				} `json:"environments"`
			} `json:"collection"`
		} `json:"collections"`
	}
	if err := json.Unmarshal(resp.Result, &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	// By handle: the listing also carries the built-in collection, and this
	// test is about the folder whose environments/ was made unreadable.
	var row *struct {
		Handle     string `json:"handle"`
		Error      string `json:"error"`
		Collection struct {
			Requests []struct {
				RelPath string `json:"relPath"`
			} `json:"requests"`
			Environments []struct {
				RelPath string `json:"relPath"`
			} `json:"environments"`
		} `json:"collection"`
	}
	for i := range listed.Collections {
		if listed.Collections[i].Handle == handle {
			row = &listed.Collections[i]
		}
	}
	if row == nil {
		t.Fatalf("collections = %+v, want the one folder still listed", listed.Collections)
	}
	if row.Error == "" {
		t.Error("the entry says nothing — an environments folder that will not read is a degrade the panel must be able to show")
	}
	// …and the half that DID read is still rendered: the requests are there.
	// Both ends of the interval — the folder is degraded, not gone.
	if len(row.Collection.Requests) == 0 {
		t.Error("the requests went with the environments — one unreadable directory emptied the folder")
	}
	if len(row.Collection.Environments) != 0 {
		t.Errorf("environments = %+v, want [] — nothing could be read", row.Collection.Environments)
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
	chosen := chosenCollections(list.Collections)
	if len(chosen) != 1 || chosen[0].Handle != made.Handle {
		t.Fatalf("opened folders = %+v, want the one just created", chosen)
	}
	if chosen[0].Error != "" {
		t.Errorf("the created folder reports %q; it must be readable", chosen[0].Error)
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
