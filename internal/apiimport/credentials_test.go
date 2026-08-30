package apiimport

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

// ONE ANSWER TO "MAY A CREDENTIAL BE IN A REQUEST FILE", asked at both doors
// in one test so the two cannot drift apart again (nocx-zn386).
//
// They had drifted. The owner decided in nocx-14exx that a credential the
// person pasted stays where they put it, and nocx-flidy rewrote the panel's
// promise to say so — a curl line's Authorization header is saved into the
// request file in full. The Postman door went on destroying the same header,
// so which answer a person got was decided by which door they came in
// through, and importing a workspace meant retyping every token in it.
const carriedToken = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlLXZhbHVl" //nolint:gosec // a synthetic token; this test is about it being carried

func TestBothImportDoorsCarryACredentialHeaderTheSameWay(t *testing.T) {
	fromCurl, unsupCurl, err := parseCurl(`curl -H 'Authorization: Bearer ` + carriedToken + `' https://api.example/x`)
	if err != nil {
		t.Fatalf("parseCurl: %v", err)
	}
	doc := `{"info":{"name":"P"},"item":[{"name":"R","request":{
	    "method":"GET","url":"https://api.example/x",
	    "header":[{"key":"Authorization","value":"Bearer ` + carriedToken + `"}]}}]}`
	_, reqs, _, unsupPostman := mustPostman(t, doc)
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	fromPostman := reqs[0]

	for _, c := range []struct {
		door  string
		req   apicoll.Request
		unsup []Unsupported
	}{
		{"curl", fromCurl, unsupCurl},
		{"postman", fromPostman, unsupPostman},
	} {
		value, ok := headerValue(c.req, "Authorization")
		if !ok || value != "Bearer "+carriedToken {
			t.Errorf("%s door: Authorization = %q %v, want the header the document carried", c.door, value, ok)
		}
		if c.req.Auth.Kind != "" && c.req.Auth.Kind != apicoll.AuthNone {
			t.Errorf("%s door: auth = %+v, want none — a header is a header, and nothing mints a variable for one", c.door, c.req.Auth)
		}
		for _, u := range c.unsup {
			if strings.Contains(strings.ToLower(u.What), "authorization") {
				t.Errorf("%s door: the carried header was reported as a loss: %+v", c.door, u)
			}
		}
	}
}

// A Postman auth BLOCK holding a live token is the same fact in the format's
// own vocabulary, and it is carried into the model's auth rather than
// itemised away.
func TestPostmanLiteralAuthIsCarriedIntoTheRequest(t *testing.T) {
	doc := `{"info":{"name":"P"},"item":[
	  {"name":"Bearer","request":{"method":"GET","url":"https://api.example/b",
	    "auth":{"type":"bearer","bearer":[{"key":"token","value":"` + carriedToken + `"}]}}},
	  {"name":"Basic","request":{"method":"GET","url":"https://api.example/c",
	    "auth":{"type":"basic","basic":[{"key":"username","value":"alice"},{"key":"password","value":"s3cr3t-p4ssw0rd"}]}}},
	  {"name":"Key","request":{"method":"GET","url":"https://api.example/d",
	    "auth":{"type":"apikey","apikey":[{"key":"key","value":"X-Api-Key"},{"key":"value","value":"live-key-value"},{"key":"in","value":"header"}]}}}]}`
	coll, reqs, _, unsup := mustPostman(t, doc)

	bearer, _ := findRequest(t, coll, reqs, "Bearer")
	if bearer.Auth.Kind != apicoll.AuthBearer || bearer.Auth.Token != carriedToken {
		t.Errorf("bearer auth = %+v, want the token the document carried", bearer.Auth)
	}
	basic, _ := findRequest(t, coll, reqs, "Basic")
	if basic.Auth.Kind != apicoll.AuthBasic || basic.Auth.User != "alice" || basic.Auth.Password != "s3cr3t-p4ssw0rd" {
		t.Errorf("basic auth = %+v, want the user and password the document carried", basic.Auth)
	}
	key, _ := findRequest(t, coll, reqs, "Key")
	if v, ok := headerValue(key, "X-Api-Key"); !ok || v != "live-key-value" {
		t.Errorf("apikey header = %q %v, want the value the document carried", v, ok)
	}
	for _, u := range unsup {
		if strings.Contains(strings.ToLower(u.Why), "credential") {
			t.Errorf("a carried credential was reported as a loss: %+v", u)
		}
	}
}

// A VARIABLE POSTMAN MARKED SECRET keeps its value too. The vault is an
// OFFER made over it (nocx-zn386's second half), never a condition of the
// import: a value that reached no file and no vault is a value the person
// has to find again somewhere else.
func TestPostmanSecretVariableKeepsItsValue(t *testing.T) {
	doc := `{"id":"eeee-1111","name":"prod","_postman_variable_scope":"environment","values":[
	  {"key":"baseUrl","value":"https://api.acme.test","type":"default","enabled":true},
	  {"key":"apiToken","value":"` + carriedToken + `","type":"secret","enabled":true}]}`
	_, _, envs, unsup := mustPostman(t, doc)
	if len(envs) != 1 {
		t.Fatalf("environments = %d, want 1", len(envs))
	}
	if envs[0].Values["apiToken"] != carriedToken {
		t.Errorf("apiToken = %q, want the value the export carried", envs[0].Values["apiToken"])
	}
	for _, u := range unsup {
		if strings.Contains(u.What, "apiToken") {
			t.Errorf("the carried variable was reported as a loss: %+v", u)
		}
	}
}

// AND THE RULE THAT DID NOT CHANGE. A `{{secret:…}}` in an imported document
// addresses a record in THIS machine's vault, and a document that arrived
// from anywhere may not name one (design §10, the owner's line; ADR-0042).
// It is dropped and said out loud, exactly as before.
func TestImportedVaultReferencesAreStillDroppedAndReported(t *testing.T) {
	const ref = "{{secret:secrow:ab12cd34}}"
	doc := `{"info":{"name":"P"},"item":[{"name":"R","request":{
	    "method":"POST","url":"https://example.test/` + ref + `",
	    "header":[{"key":"X-Token","value":"Bearer ` + ref + `"}],
	    "body":{"mode":"raw","raw":"payload=` + ref + `"}}}]}`
	_, reqs, _, unsup := mustPostman(t, doc)
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	named := 0
	for _, u := range unsup {
		if strings.Contains(u.What, ref) {
			named++
		}
	}
	if named < 3 {
		t.Fatalf("unsupported = %+v, want the url, the header and the body named", unsup)
	}
	if v, ok := headerValue(reqs[0], "X-Token"); ok && strings.Contains(v, ref) {
		t.Fatalf("the reference survived into the header: %q", v)
	}
	if strings.Contains(reqs[0].URL, ref) || strings.Contains(reqs[0].Body.Text, ref) {
		t.Fatal("the reference survived into the request")
	}
}

// THE OFFER'S TWO HALVES, and the seam between them. This package answers
// WHICH variables the export marked secret; the caller — which holds the
// vault gate, because this one does not — mints the records and hands back
// the references, and only then does a reference reach a file.
func TestSecretRefsReplaceTheValueTheExportCarried(t *testing.T) {
	doc := `{"id":"e1","name":"prod","_postman_variable_scope":"environment","values":[
	  {"key":"baseUrl","value":"https://api.acme.test","type":"default","enabled":true},
	  {"key":"apiToken","value":"` + carriedToken + `","type":"secret","enabled":true}]}`

	offered, err := PostmanSecrets(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("PostmanSecrets: %v", err)
	}
	if len(offered) != 1 || offered[0].Name != "apiToken" || offered[0].Value != carriedToken {
		t.Fatalf("offered = %+v, want apiToken with the value the export carried", offered)
	}

	const ref = "{{secret:secrow:ab12cd34}}"
	dest := destUnder(t)
	if _, err := ImportInto(t.Context(), NewOSFS(), dest, strings.NewReader(doc), apicoll.Route{},
		SecretRefs{"apiToken": ref}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}
	files := walkFiles(t, dest)
	env, ok := files["environments/prod.json"]
	if !ok {
		t.Fatalf("no prod environment; have %v", keysOf(files))
	}
	if !strings.Contains(env, ref) {
		t.Fatalf("the environment does not carry the reference: %s", env)
	}
	for name, body := range files {
		if strings.Contains(body, carriedToken) {
			t.Fatalf("%s still carries the value the vault now holds", name)
		}
	}
	// The ordinary variable is untouched: the offer is about the ones
	// Postman marked, and nothing else moves because of it.
	if !strings.Contains(env, "https://api.acme.test") {
		t.Fatalf("the ordinary value was disturbed: %s", env)
	}
}

// AN ARCHIVE'S REFERENCES ARE KEYED BY MEMBER PATH. Two documents in one
// export may declare the same variable name holding different values, and a
// map keyed by name would hand the second one the first one's record.
func TestArchiveSecretRefsAreAppliedPerDocument(t *testing.T) {
	const other = "second-document-token-0a1b2c3d4e5f"
	archive := makePostmanArchive(t, map[string]string{
		"archive.json": `{"environment":{"env-1":true,"env-2":true}}`,
		"environment/env-1.json": `{"id":"env-1","name":"one","values":[
		  {"key":"apiToken","value":"` + carriedToken + `","type":"secret","enabled":true}]}`,
		"environment/env-2.json": `{"id":"env-2","name":"two","values":[
		  {"key":"apiToken","value":"` + other + `","type":"secret","enabled":true}]}`,
	})
	documents, err := ReadPostmanArchive(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("ReadPostmanArchive: %v", err)
	}
	refs := ArchiveSecretRefs{}
	for _, doc := range documents {
		if len(doc.Secrets) != 1 || doc.Secrets[0].Name != "apiToken" {
			t.Fatalf("%s offered %+v, want one apiToken", doc.Path, doc.Secrets)
		}
		refs[doc.Path] = SecretRefs{"apiToken": "{{secret:secrow:" + doc.Name + "}}"}
	}

	dest := destUnder(t) + "-archive"
	if _, err := ImportPostmanArchive(t.Context(), NewOSFS(), dest, bytes.NewReader(archive), apicoll.Route{}, refs); err != nil {
		t.Fatalf("ImportPostmanArchive: %v", err)
	}
	files := walkFiles(t, dest)
	for name, body := range files {
		if strings.Contains(body, carriedToken) || strings.Contains(body, other) {
			t.Fatalf("%s carries a value the vault now holds", name)
		}
	}
	joined := strings.Join(valuesOf(files), "\n")
	for _, want := range []string{"{{secret:secrow:one}}", "{{secret:secrow:two}}"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("no file carries %s; have %v", want, keysOf(files))
		}
	}
}
