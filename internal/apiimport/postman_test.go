package apiimport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

// The secret and its identifier. Both are asserted absent from everything
// the import produces; the value is what the vault holds and the id is what
// Postman's export calls it, and neither may reach a file (§8).
//
// gosec is right that these are hardcoded credential-shaped strings, and
// that is the point: a fixture that did not look like a credential could
// not test the rule that no credential reaches a file. Renaming them to
// something outside gosec's pattern would silence the finding and lose the
// only place the reader is told what they are, so they are suppressed with
// the reason attached instead.
const (
	pmSecretValue = "pm-live-9f3a7c21bd4e8a06f5c1"         //nolint:gosec // a synthetic credential; the tests assert it reaches no file
	pmSecretID    = "d4f1a2b3-0000-4c5d-9e8f-112233445566" //nolint:gosec // a synthetic identifier; the tests assert it reaches no file
)

// postmanFixture is one export exercising every branch the converter has:
// nested folders, a secret variable, templated and literal auth, each body
// mode, scripts, saved responses, an auth type with no home in the model,
// a form file part, and a folder name that is a path traversal.
const postmanFixture = `{
  "info": {
    "_postman_id": "11112222-3333-4444-5555-666677778888",
    "name": "Acme API",
    "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"
  },
  "auth": { "type": "bearer", "bearer": [ { "key": "token", "value": "{{apiToken}}", "type": "string" } ] },
  "event": [ { "listen": "prerequest", "script": { "exec": ["pm.environment.set('x', 1)"], "type": "text/javascript" } } ],
  "variable": [
    { "id": "aaaa-1111", "key": "baseUrl", "value": "https://api.acme.test", "type": "default" },
    { "id": "` + pmSecretID + `", "key": "apiToken", "value": "` + pmSecretValue + `", "type": "secret" },
    { "id": "cccc-3333", "key": "legacy", "value": "off", "type": "default", "disabled": true }
  ],
  "item": [
    {
      "name": "Users",
      "item": [
        {
          "name": "Create user",
          "request": {
            "method": "POST",
            "header": [
              { "key": "Content-Type", "value": "application/json" },
              { "key": "X-Legacy", "value": "1", "disabled": true }
            ],
            "body": { "mode": "raw", "raw": "{\"email\":\"a@b.c\"}", "options": { "raw": { "language": "json" } } },
            "url": { "raw": "{{baseUrl}}/users", "host": ["{{baseUrl}}"], "path": ["users"] },
            "description": "Creates a user."
          },
          "response": [ { "name": "201", "body": "{}" } ]
        },
        {
          "name": "List users",
          "request": {
            "method": "GET",
            "url": {
              "raw": "{{baseUrl}}/users?page=1&q=a b",
              "host": ["{{baseUrl}}"],
              "path": ["users"],
              "query": [
                { "key": "page", "value": "1" },
                { "key": "q", "value": "a b" },
                { "key": "internal", "value": "1", "disabled": true }
              ]
            }
          },
          "event": [ { "listen": "test", "script": { "exec": ["pm.test('ok', function(){})"] } } ]
        }
      ]
    },
    {
      "name": "Forms",
      "item": [
        {
          "name": "Urlencoded",
          "request": {
            "method": "POST",
            "body": { "mode": "urlencoded", "urlencoded": [ { "key": "a", "value": "1" }, { "key": "b", "value": "x y" }, { "key": "c", "value": "2", "disabled": true } ] },
            "url": "{{baseUrl}}/form"
          }
        },
        {
          "name": "Multipart",
          "request": {
            "method": "POST",
            "body": { "mode": "formdata", "formdata": [ { "key": "name", "value": "alice", "type": "text" }, { "key": "avatar", "src": "/home/me/a.png", "type": "file" } ] },
            "url": "{{baseUrl}}/upload"
          }
        },
        {
          "name": "Binary",
          "request": {
            "method": "PUT",
            "body": { "mode": "file", "file": { "src": "payload.bin" } },
            "url": "{{baseUrl}}/blob"
          }
        },
        {
          "name": "Graph",
          "request": {
            "method": "POST",
            "body": { "mode": "graphql", "graphql": { "query": "{ me { id } }", "variables": "{}" } },
            "url": "{{baseUrl}}/graphql"
          }
        }
      ]
    },
    {
      "name": "../../etc",
      "item": [
        { "name": "passwd", "request": { "method": "GET", "url": "{{baseUrl}}/x" } }
      ]
    },
    {
      "name": "Literal token",
      "request": {
        "method": "GET",
        "auth": { "type": "bearer", "bearer": [ { "key": "token", "value": "` + pmSecretValue + `", "type": "string" } ] },
        "url": "{{baseUrl}}/whoami"
      }
    },
    {
      "name": "Api key",
      "request": {
        "method": "GET",
        "auth": { "type": "apikey", "apikey": [ { "key": "key", "value": "X-Api-Key" }, { "key": "value", "value": "{{apiToken}}" }, { "key": "in", "value": "header" } ] },
        "url": "{{baseUrl}}/keyed"
      }
    },
    {
      "name": "Oauthed",
      "request": {
        "method": "GET",
        "auth": { "type": "oauth2", "oauth2": [ { "key": "accessToken", "value": "` + pmSecretValue + `" } ] },
        "url": "{{baseUrl}}/oauthed"
      }
    },
    {
      "name": "No auth",
      "request": { "method": "GET", "auth": { "type": "noauth" }, "url": "{{baseUrl}}/public" }
    }
  ]
}`

func mustPostman(t *testing.T, doc string) (apicoll.Collection, []apicoll.Request, []apicoll.Environment, []Unsupported) {
	t.Helper()
	res, err := parsePostman(strings.NewReader(doc), apicoll.Route{})
	if err != nil {
		t.Fatalf("parsePostman: %v", err)
	}
	return res.Collection, res.Requests, res.Environments, res.Unsupported
}

func findRequest(t *testing.T, coll apicoll.Collection, reqs []apicoll.Request, name string) (apicoll.Request, apicoll.RequestRef) {
	t.Helper()
	for i, r := range reqs {
		if r.Name == name {
			return r, coll.Requests[i]
		}
	}
	t.Fatalf("no request named %q; have %v", name, requestNames(reqs))
	return apicoll.Request{}, apicoll.RequestRef{}
}

func requestNames(reqs []apicoll.Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Name)
	}
	return out
}

// The refs and the requests are one list read two ways: coll.Requests[i]
// says where requests[i] goes. Nothing else ties a request to its path, so
// this is the invariant the writer depends on.
func TestPostmanRefsAndRequestsAreParallel(t *testing.T) {
	coll, reqs, _, _ := mustPostman(t, postmanFixture)
	if len(coll.Requests) != len(reqs) {
		t.Fatalf("%d refs for %d requests", len(coll.Requests), len(reqs))
	}
	if len(reqs) == 0 {
		t.Fatal("no requests imported")
	}
	seen := map[string]bool{}
	for i, ref := range coll.Requests {
		if ref.Name != reqs[i].Name {
			t.Fatalf("ref %d names %q, request names %q", i, ref.Name, reqs[i].Name)
		}
		if ref.Method != reqs[i].Method {
			t.Fatalf("ref %d method %q, request method %q", i, ref.Method, reqs[i].Method)
		}
		if !strings.HasSuffix(ref.RelPath, ".json") {
			t.Fatalf("ref %d path %q is not a file", i, ref.RelPath)
		}
		if seen[ref.RelPath] {
			t.Fatalf("two requests share the path %q", ref.RelPath)
		}
		seen[ref.RelPath] = true
	}
	if coll.Name != "Acme API" {
		t.Fatalf("collection name = %q", coll.Name)
	}
}

func TestPostmanFoldersBecomeDirectories(t *testing.T) {
	coll, reqs, _, _ := mustPostman(t, postmanFixture)
	_, ref := findRequest(t, coll, reqs, "Create user")
	if !strings.HasPrefix(ref.RelPath, "Users/") {
		t.Fatalf("RelPath = %q, want it under the Users folder", ref.RelPath)
	}
	if strings.Count(ref.RelPath, "/") != 1 {
		t.Fatalf("RelPath = %q, want exactly one folder level", ref.RelPath)
	}
	// A top-level request sits at the root.
	_, ref2 := findRequest(t, coll, reqs, "No auth")
	if strings.Contains(ref2.RelPath, "/") {
		t.Fatalf("RelPath = %q, want the collection root", ref2.RelPath)
	}
}

// A folder name out of a pull request is a path, and a path is hostile
// (§13.1). Refused-or-rewritten is the choice; here the name is a display
// string and the PATH is minted by us, so the traversal simply cannot be
// spelled.
func TestPostmanHostileFolderNameCannotEscape(t *testing.T) {
	coll, reqs, _, _ := mustPostman(t, postmanFixture)
	_, ref := findRequest(t, coll, reqs, "passwd")
	if strings.Contains(ref.RelPath, "..") {
		t.Fatalf("RelPath = %q contains a traversal", ref.RelPath)
	}
	if strings.HasPrefix(ref.RelPath, "/") {
		t.Fatalf("RelPath = %q is absolute", ref.RelPath)
	}
	for _, r := range coll.Requests {
		for _, seg := range strings.Split(r.RelPath, "/") {
			if seg == "." || seg == ".." || seg == "" {
				t.Fatalf("RelPath %q has the segment %q", r.RelPath, seg)
			}
		}
	}
}

func TestPostmanTemplatesSurvive(t *testing.T) {
	coll, reqs, _, _ := mustPostman(t, postmanFixture)
	req, _ := findRequest(t, coll, reqs, "Create user")
	if req.URL != "{{baseUrl}}/users" {
		t.Fatalf("URL = %q, want {{baseUrl}} intact", req.URL)
	}
	list, _ := findRequest(t, coll, reqs, "List users")
	if list.URL != "{{baseUrl}}/users" {
		t.Fatalf("URL = %q", list.URL)
	}
}

func TestPostmanSecretVariableBecomesANameOnly(t *testing.T) {
	_, reqs, envs, _ := mustPostman(t, postmanFixture)
	if len(envs) != 1 {
		t.Fatalf("%d environments, want 1", len(envs))
	}
	env := envs[0]
	if env.Route.Kind != apicoll.RouteDirect {
		t.Fatalf("route = %+v, want direct", env.Route)
	}
	found := false
	for _, v := range env.SecretVars {
		if v == "apiToken" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SecretVars = %v, want apiToken", env.SecretVars)
	}
	if _, ok := env.Values["apiToken"]; ok {
		t.Fatal("the secret variable also landed in Values")
	}
	if env.Values["baseUrl"] != "https://api.acme.test" {
		t.Fatalf("baseUrl = %q", env.Values["baseUrl"])
	}

	// The rule, asserted over everything the converter returns: not the
	// value, not Postman's id for it.
	blob, err := json.Marshal(struct {
		R []apicoll.Request
		E []apicoll.Environment
	}{reqs, envs})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), pmSecretValue) {
		t.Fatal("the secret VALUE is in the converted model")
	}
	if strings.Contains(string(blob), pmSecretID) {
		t.Fatal("an IDENTIFIER for the secret is in the converted model")
	}
}

// A collection whose request auth carries a live token — not a template —
// still writes only a variable reference.
func TestPostmanLiteralTokenBecomesAVariable(t *testing.T) {
	coll, reqs, _, _ := mustPostman(t, postmanFixture)
	req, _ := findRequest(t, coll, reqs, "Literal token")
	if req.Auth.Kind != apicoll.AuthBearer {
		t.Fatalf("auth = %+v", req.Auth)
	}
	if req.Auth.Token == "" {
		t.Fatal("auth names no variable")
	}
	assertAbsent(t, req, pmSecretValue)
}

func TestPostmanAuthInheritance(t *testing.T) {
	coll, reqs, _, _ := mustPostman(t, postmanFixture)
	// No auth of its own: the collection's bearer applies.
	req, _ := findRequest(t, coll, reqs, "Create user")
	if req.Auth.Kind != apicoll.AuthBearer || req.Auth.Token != "{{apiToken}}" {
		t.Fatalf("inherited auth = %+v", req.Auth)
	}
	// noauth overrides the inheritance rather than being ignored.
	pub, _ := findRequest(t, coll, reqs, "No auth")
	if pub.Auth.Kind != apicoll.AuthNone {
		t.Fatalf("noauth = %+v", pub.Auth)
	}
}

// apikey has a header name, and the model's Auth has nowhere to put one —
// so it becomes the header it actually is, rather than a second field
// meaning the same thing.
func TestPostmanApiKeyAuthBecomesAHeader(t *testing.T) {
	coll, reqs, _, _ := mustPostman(t, postmanFixture)
	req, _ := findRequest(t, coll, reqs, "Api key")
	v, ok := headerValue(req, "X-Api-Key")
	if !ok {
		t.Fatalf("headers = %+v", req.Headers)
	}
	if v != "{{apiToken}}" {
		t.Fatalf("X-Api-Key = %q", v)
	}
}

func TestPostmanUnsupportedAuthIsItemisedAndNeverWritten(t *testing.T) {
	coll, reqs, _, unsup := mustPostman(t, postmanFixture)
	req, _ := findRequest(t, coll, reqs, "Oauthed")
	if req.Auth.Kind != apicoll.AuthNone {
		t.Fatalf("auth = %+v, want none", req.Auth)
	}
	assertAbsent(t, req, pmSecretValue)
	if !anyUnsupportedContaining(unsup, "oauth2") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
}

func TestPostmanQueryAndDisabledRows(t *testing.T) {
	coll, reqs, _, _ := mustPostman(t, postmanFixture)
	req, _ := findRequest(t, coll, reqs, "List users")
	if v, ok := queryValue(req, "page"); !ok || v != "1" {
		t.Fatalf("page = %q %v", v, ok)
	}
	if v, ok := queryValue(req, "q"); !ok || v != "a b" {
		t.Fatalf("q = %q %v", v, ok)
	}
	// A disabled row is a row the user keeps (apicoll's own reason for the
	// Enabled field) — carried, not deleted, and not enabled.
	var internal *apicoll.Param
	for i := range req.Query {
		if req.Query[i].Name == "internal" {
			internal = &req.Query[i]
		}
	}
	if internal == nil {
		t.Fatalf("the disabled query row was dropped: %+v", req.Query)
	}
	if internal.Enabled {
		t.Fatal("the disabled query row came back enabled")
	}

	create, _ := findRequest(t, coll, reqs, "Create user")
	var legacy *apicoll.Header
	for i := range create.Headers {
		if create.Headers[i].Name == "X-Legacy" {
			legacy = &create.Headers[i]
		}
	}
	if legacy == nil || legacy.Enabled {
		t.Fatalf("disabled header = %+v", create.Headers)
	}
}

func TestPostmanBodyModes(t *testing.T) {
	coll, reqs, _, unsup := mustPostman(t, postmanFixture)

	raw, _ := findRequest(t, coll, reqs, "Create user")
	if raw.Body.Kind != apicoll.BodyRaw || raw.Body.Text != `{"email":"a@b.c"}` {
		t.Fatalf("raw body = %+v", raw.Body)
	}

	form, _ := findRequest(t, coll, reqs, "Urlencoded")
	if form.Body.Kind != apicoll.BodyForm || form.Body.Text != "a=1&b=x+y" {
		t.Fatalf("urlencoded body = %+v (a disabled row must not be sent)", form.Body)
	}

	multi, _ := findRequest(t, coll, reqs, "Multipart")
	if multi.Body.Kind != apicoll.BodyForm || multi.Body.Text != "name=alice" {
		t.Fatalf("formdata body = %+v", multi.Body)
	}
	if !anyUnsupportedContaining(unsup, "file part") {
		t.Fatalf("the multipart file part was not itemised: %v", unsupportedWhat(unsup))
	}
	if !anyUnsupportedContaining(unsup, "multipart") {
		t.Fatalf("converting multipart to urlencoded was not said out loud: %v", unsupportedWhat(unsup))
	}

	bin, _ := findRequest(t, coll, reqs, "Binary")
	if bin.Body.Kind != apicoll.BodyFile || bin.Body.FileRef != "payload.bin" {
		t.Fatalf("file body = %+v", bin.Body)
	}

	gql, _ := findRequest(t, coll, reqs, "Graph")
	if gql.Body.Kind != apicoll.BodyRaw || !strings.Contains(gql.Body.Text, "me { id }") {
		t.Fatalf("graphql body = %+v", gql.Body)
	}
}

func TestPostmanScriptsAndSavedResponsesAreItemised(t *testing.T) {
	_, _, _, unsup := mustPostman(t, postmanFixture)
	if !anyUnsupportedContaining(unsup, "script") {
		t.Fatalf("scripts were not itemised: %v", unsupportedWhat(unsup))
	}
	if !anyUnsupportedContaining(unsup, "saved response") {
		t.Fatalf("saved responses were not itemised: %v", unsupportedWhat(unsup))
	}
	if !anyUnsupportedContaining(unsup, "disabled variable") {
		t.Fatalf("the disabled collection variable was not itemised: %v", unsupportedWhat(unsup))
	}
	// Every entry says what and why; an entry with no why is a log line
	// wearing a return value's clothes.
	for _, u := range unsup {
		if u.What == "" || u.Why == "" {
			t.Fatalf("incomplete entry %+v", u)
		}
		if strings.Contains(u.What, pmSecretValue) || strings.Contains(u.Why, pmSecretValue) {
			t.Fatalf("an entry echoed the secret: %+v", u)
		}
	}
}

func anyUnsupportedContaining(unsup []Unsupported, needle string) bool {
	for _, u := range unsup {
		if strings.Contains(strings.ToLower(u.What), strings.ToLower(needle)) ||
			strings.Contains(strings.ToLower(u.Why), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// A Postman ENVIRONMENT export is the other document this format has, and
// it is where a secret variable most often lives.
func TestPostmanEnvironmentExport(t *testing.T) {
	const doc = `{
      "id": "eeee-1111",
      "name": "prod",
      "values": [
        { "key": "baseUrl", "value": "https://api.acme.test", "type": "default", "enabled": true },
        { "key": "apiToken", "value": "` + pmSecretValue + `", "type": "secret", "enabled": true },
        { "key": "off", "value": "x", "type": "default", "enabled": false }
      ],
      "_postman_variable_scope": "environment"
    }`
	coll, reqs, envs, unsup := mustPostman(t, doc)
	if len(reqs) != 0 || len(coll.Requests) != 0 {
		t.Fatalf("an environment export produced %d requests", len(reqs))
	}
	if len(envs) != 1 || envs[0].Name != "prod" {
		t.Fatalf("envs = %+v", envs)
	}
	if envs[0].Values["baseUrl"] != "https://api.acme.test" {
		t.Fatalf("values = %+v", envs[0].Values)
	}
	if len(envs[0].SecretVars) != 1 || envs[0].SecretVars[0] != "apiToken" {
		t.Fatalf("secretVars = %v", envs[0].SecretVars)
	}
	if _, ok := envs[0].Values["apiToken"]; ok {
		t.Fatal("the secret value landed in Values")
	}
	if !anyUnsupportedContaining(unsup, "disabled variable") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	blob, _ := json.Marshal(envs)
	if strings.Contains(string(blob), pmSecretValue) || strings.Contains(string(blob), "eeee-1111") {
		t.Fatalf("the environment carries a value or an id: %s", blob)
	}
}

// ---- hostile input: every one of these is a document somebody can hand us ----

type endlessSpaces struct{ n int }

func (e *endlessSpaces) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	e.n += len(p)
	return len(p), nil
}

func TestPostmanRejects(t *testing.T) {
	deepOpen := strings.Repeat(`{"name":"f","item":[`, 200)
	deepClose := strings.Repeat(`]}`, 200)
	cases := []struct {
		name string
		doc  string
	}{
		{"empty", ``},
		{"not json", `curl https://x`},
		{"truncated", `{"info":{"name":"x"},"item":[`},
		{"trailing data", `{"info":{"name":"x"},"item":[]} {"more":1}`},
		{"a json array", `[]`},
		{"a json string", `"hello"`},
		{"neither a collection nor an environment", `{"hello":"world"}`},
		{"folders nested past the limit", `{"info":{"name":"x"},"item":[` + deepOpen + deepClose + `]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parsePostman(strings.NewReader(tc.doc), apicoll.Route{}); err == nil {
				t.Fatalf("parsePostman(%.40q, apicoll.Route{}) succeeded, want an error", tc.doc)
			}
		})
	}
}

func TestPostmanRefusesAnEndlessDocument(t *testing.T) {
	if _, err := parsePostman(&endlessSpaces{}, apicoll.Route{}); err == nil {
		t.Fatal("an endless document was accepted")
	}
}

// The paired success: the smallest real collection imports.
func TestPostmanMinimalCollectionSucceeds(t *testing.T) {
	const doc = `{"info":{"name":"Tiny","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
	              "item":[{"name":"Ping","request":"https://api.acme.test/ping"}]}`
	coll, reqs, envs, unsup := mustPostman(t, doc)
	if coll.Name != "Tiny" || len(reqs) != 1 {
		t.Fatalf("coll = %+v reqs = %+v", coll, reqs)
	}
	if reqs[0].Method != "GET" || reqs[0].URL != "https://api.acme.test/ping" {
		t.Fatalf("req = %+v", reqs[0])
	}
	if len(envs) != 0 {
		t.Fatalf("a collection with no variables produced %d environments", len(envs))
	}
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
}

// Two requests with the same name in one folder are two files, not one
// file written twice.
func TestPostmanDuplicateNamesGetDistinctPaths(t *testing.T) {
	const doc = `{"info":{"name":"Dup"},"item":[
	   {"name":"Same","request":"https://a.test/1"},
	   {"name":"Same","request":"https://a.test/2"},
	   {"name":"Same","request":"https://a.test/3"}]}`
	coll, reqs, _, _ := mustPostman(t, doc)
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	seen := map[string]bool{}
	for _, r := range coll.Requests {
		if seen[r.RelPath] {
			t.Fatalf("duplicate path %q", r.RelPath)
		}
		seen[r.RelPath] = true
	}
}

// A name made entirely of characters a filename cannot hold still produces
// a file, and the NAME the user sees is untouched.
func TestPostmanUnnameableItemStillGetsAFile(t *testing.T) {
	const doc = `{"info":{"name":"Odd"},"item":[
	   {"name":"///","request":"https://a.test/1"},
	   {"name":"","request":"https://a.test/2"}]}`
	coll, reqs, _, _ := mustPostman(t, doc)
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	if reqs[0].Name != "///" {
		t.Fatalf("the display name was rewritten: %q", reqs[0].Name)
	}
	for _, r := range coll.Requests {
		if r.RelPath == ".json" || strings.HasPrefix(r.RelPath, "/") || r.RelPath == "" {
			t.Fatalf("RelPath = %q", r.RelPath)
		}
	}
}

func TestPostmanRequestIDsAreDeterministicAndUnique(t *testing.T) {
	_, a, _, _ := mustPostman(t, postmanFixture)
	_, b, _, _ := mustPostman(t, postmanFixture)
	seen := map[string]bool{}
	for i := range a {
		if a[i].ID == "" {
			t.Fatalf("request %q has no id", a[i].Name)
		}
		if a[i].ID != b[i].ID {
			t.Fatalf("id for %q is not stable: %q vs %q", a[i].Name, a[i].ID, b[i].ID)
		}
		if seen[a[i].ID] {
			t.Fatalf("duplicate id %q", a[i].ID)
		}
		seen[a[i].ID] = true
	}
}

// Postman marks perhaps half of the tokens people keep in it as "secret".
// A collection is meant to be committed, so a live token in a variable
// nobody marked is the exact failure §8 exists to prevent — it is treated
// as secret and the promotion is said out loud rather than done quietly.
func TestPostmanUnmarkedCredentialIsPromotedAndSaidOutLoud(t *testing.T) {
	const pat = "ghp_0123456789abcdefghijklmnopqrstuvwx"
	doc := `{"info":{"name":"P"},"variable":[
	    {"key":"legacyToken","value":"` + pat + `","type":"default"},
	    {"key":"baseUrl","value":"https://api.acme.test","type":"default"}],
	   "item":[{"name":"Ping","request":"{{baseUrl}}/ping"}]}`
	_, _, envs, unsup := mustPostman(t, doc)
	if len(envs) != 1 {
		t.Fatalf("envs = %+v", envs)
	}
	if len(envs[0].SecretVars) != 1 || envs[0].SecretVars[0] != "legacyToken" {
		t.Fatalf("secretVars = %v", envs[0].SecretVars)
	}
	if _, ok := envs[0].Values["legacyToken"]; ok {
		t.Fatal("the credential landed in Values")
	}
	if envs[0].Values["baseUrl"] != "https://api.acme.test" {
		t.Fatalf("the ordinary variable was promoted too: %+v", envs[0].Values)
	}
	if !anyUnsupportedContaining(unsup, "not marked secret") {
		t.Fatalf("the promotion was silent: %v", unsupportedWhat(unsup))
	}
	blob, _ := json.Marshal(envs)
	if strings.Contains(string(blob), pat) {
		t.Fatalf("the credential is in the environment: %s", blob)
	}
}

// A Postman path variable — `/users/:id` with its value beside the address —
// is CARRIED now, and the import says what it did (nocx-kprt4.3).
//
// It used to be dropped in silence, then reported as a loss: there was
// nowhere in the model for `id = 54321` to go, so the address kept `:id` and
// nothing could resolve it. The request has variables of its own now, so the
// same import is a reported CHANGE — one grammar, `{{name}}`, and the value
// in the request's own table.
func TestPostmanPathVariablesBecomeTheRequestsOwnVariables(t *testing.T) {
	const doc = `{
      "info": {"name": "PV", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
      "item": [
        {"name": "One user", "request": {"method": "GET", "url": {
           "raw": "https://example.test/users/:id",
           "variable": [{"key": "id", "value": "54321"}]}}},
        {"name": "Templated", "request": {"method": "GET", "url": {
           "raw": "https://example.test/orders/:orderId/items"}}}
      ]}`
	coll, reqs, _, unsup := mustPostman(t, doc)

	// ONE GRAMMAR: the address holds `{{id}}` and never `:id`, because two
	// spellings of one hole would be two owners of it.
	req, _ := findRequest(t, coll, reqs, "One user")
	if req.URL != "https://example.test/users/{{id}}" {
		t.Fatalf("url = %q, want the `:id` segment rewritten into our grammar", req.URL)
	}
	// AND THE VALUE CAME WITH IT, into the request's own table.
	if len(req.Variables) != 1 {
		t.Fatalf("variables = %+v, want the one the address uses", req.Variables)
	}
	if got := req.Variables[0]; got.Name != "id" || got.Value != "54321" || !got.Enabled {
		t.Errorf("variable = %+v, want id=54321 enabled", got)
	}

	// A `:name` WITH NO VALUE is still rewritten, with an empty value beside
	// it: the panel gets a row to fill, and the send refuses by naming the
	// variable rather than dialling a URL with a colon segment in it.
	templated, _ := findRequest(t, coll, reqs, "Templated")
	if templated.URL != "https://example.test/orders/{{orderId}}/items" {
		t.Fatalf("url = %q, want the segment rewritten", templated.URL)
	}
	if len(templated.Variables) != 1 || templated.Variables[0].Name != "orderId" {
		t.Fatalf("variables = %+v, want an empty row named orderId", templated.Variables)
	}
	if templated.Variables[0].Value != "" {
		t.Errorf("value = %q, want empty — the export carried none", templated.Variables[0].Value)
	}

	// AND IT IS REPORTED. What the format could not carry is named
	// afterwards; so is what it carried DIFFERENTLY, because an address a
	// person wrote as `:id` and reads back as `{{id}}` is a change they
	// should not have to discover.
	if !anyUnsupportedContaining(unsup, "path variables") || !anyUnsupportedContaining(unsup, "id") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if !anyUnsupportedContaining(unsup, "{{name}}") {
		t.Fatalf("unsupported = %v, want the new spelling named", unsupportedWhat(unsup))
	}
}

// A DECLARED VARIABLE THE ADDRESS NEVER USES is reported and not carried.
// Inventing a row for it would put a value in a file under a name nothing
// reads — and the person would have no way to tell it apart from a hole they
// still have to fill.
func TestPostmanAPathVariableTheAddressNeverUsesIsReported(t *testing.T) {
	const doc = `{
      "info": {"name": "PV", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
      "item": [{"name": "Plain", "request": {"method": "GET", "url": {
         "raw": "https://example.test/users",
         "variable": [{"key": "id", "value": "54321"}]}}}]}`
	coll, reqs, _, unsup := mustPostman(t, doc)

	req, _ := findRequest(t, coll, reqs, "Plain")
	if len(req.Variables) != 0 {
		t.Errorf("variables = %+v, want none — the address uses no hole", req.Variables)
	}
	if req.URL != "https://example.test/users" {
		t.Errorf("url = %q, want it untouched", req.URL)
	}
	if !anyUnsupportedContaining(unsup, "never used them") {
		t.Fatalf("unsupported = %v, want the unused declaration named", unsupportedWhat(unsup))
	}
}

// A URL POSTMAN WROTE AS A BARE STRING carries no `variable` list beside it
// and can still spell a `:name`. It is rewritten too — otherwise the same
// export written two legal ways would import two different addresses.
func TestPostmanAColonSegmentInAStringURLIsRewrittenToo(t *testing.T) {
	const doc = `{
      "info": {"name": "PV", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
      "item": [{"name": "Bare", "request": {"method": "GET",
        "url": "https://example.test/users/:id/orders"}}]}`
	coll, reqs, _, _ := mustPostman(t, doc)

	req, _ := findRequest(t, coll, reqs, "Bare")
	if req.URL != "https://example.test/users/{{id}}/orders" {
		t.Fatalf("url = %q", req.URL)
	}
	if len(req.Variables) != 1 || req.Variables[0].Name != "id" {
		t.Fatalf("variables = %+v", req.Variables)
	}
}

// The scheme's own colon and a port are not path variables. Reporting them
// would put a line on every ordinary import, which is how a list of real
// losses stops being read.
func TestPostmanOrdinaryURLIsNotReportedAsTemplated(t *testing.T) {
	const doc = `{
      "info": {"name": "PV", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
      "item": [{"name": "Plain", "request": {"method": "GET",
        "url": {"raw": "https://example.test:8443/users/42"}}}]}`
	_, _, _, unsup := mustPostman(t, doc)
	if anyUnsupportedContaining(unsup, "templated path") || anyUnsupportedContaining(unsup, "path variables") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
}

// The route the document ARRIVED through is the route the environment it
// mints leaves by (§6): a collection fetched from behind a bastion whose
// environment says `direct` is a collection where every request fails until
// the person sets by hand the thing they had already told the import.
func TestImportIntoEnvironmentCarriesTheRouteTheDocumentArrivedThrough(t *testing.T) {
	dest := destUnder(t)
	doc := `{"info":{"name":"acme","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[],"variable":[{"key":"baseUrl","value":"https://acme.test"}]}`

	if _, err := ImportInto(t.Context(), newProbeFS(), &recordingBinder{}, dest, strings.NewReader(doc),
		apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: "prod-bastion", InsecureTLS: true}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}

	files := walkFiles(t, dest)
	body, ok := files["environments/default.json"]
	if !ok {
		t.Fatalf("no environments/default.json; have %v", keysOf(files))
	}
	var env apicoll.Environment
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("unmarshal the environment: %v", err)
	}
	if env.Route.Kind != apicoll.RouteConnection {
		t.Errorf("route kind = %q, want %q", env.Route.Kind, apicoll.RouteConnection)
	}
	if env.Route.ProfileID != "prod-bastion" {
		t.Errorf("profile = %q, want prod-bastion", env.Route.ProfileID)
	}
	// InsecureTLS is per-ENVIRONMENT on purpose (collection.go:126): a
	// fetch is not an environment, so it may not turn verification off for
	// every request the collection will ever send.
	if env.Route.InsecureTLS {
		t.Error("insecureTls was carried in from the fetch route; it must never be")
	}
}

// And the zero route is `direct`, which is what every caller that did not
// fetch anything passes.
func TestImportIntoZeroRouteWritesDirect(t *testing.T) {
	dest := destUnder(t)
	doc := `{"info":{"name":"acme","schema":"https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},"item":[],"variable":[{"key":"baseUrl","value":"https://acme.test"}]}`

	if _, err := ImportInto(t.Context(), newProbeFS(), &recordingBinder{}, dest, strings.NewReader(doc), apicoll.Route{}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}

	var env apicoll.Environment
	if err := json.Unmarshal([]byte(walkFiles(t, dest)["environments/default.json"]), &env); err != nil {
		t.Fatalf("unmarshal the environment: %v", err)
	}
	if env.Route.Kind != apicoll.RouteDirect {
		t.Errorf("route kind = %q, want %q", env.Route.Kind, apicoll.RouteDirect)
	}
	if env.Route.ProfileID != "" {
		t.Errorf("profile = %q, want empty", env.Route.ProfileID)
	}
}
