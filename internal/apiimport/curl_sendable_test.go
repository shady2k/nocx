package apiimport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/apisend"
)

// headerRowsOf is the whole Headers table as "Name: Value" rows, in order,
// because ORDER is half of what these tests are about.
func headerRowsOf(req apicoll.Request) []string {
	out := make([]string, 0, len(req.Headers))
	for _, h := range req.Headers {
		out = append(out, h.Name+": "+h.Value)
	}
	return out
}

// Every header the line carries, in the order it gave them — including the
// Authorization header, which this entrance no longer absorbs into a
// variable nobody can bind.
func TestCurlKeepsEveryHeaderInTheOrderGiven(t *testing.T) {
	req, _ := mustCurl(t, `curl -X POST http://127.0.0.1:8080/v1/broker-access `+
		`-H "Authorization: Bearer TOKEN" -H "Content-Type: application/json" `+
		`-H "X-Trace: abc" -d '{"broker":"tinkoff"}'`)

	want := []string{
		"Authorization: Bearer TOKEN",
		"Content-Type: application/json",
		"X-Trace: abc",
	}
	got := headerRowsOf(req)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("headers =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// nocx-14exx, through the seam a person reaches: import the line, compose
// it the way the send handler composes it, and send it. The assertion is
// that the exchange was ATTEMPTED — the server saw the request — rather
// than refused at compose over a variable nobody could bind.
func TestCurlImportedRequestSendsOnNoEnvironment(t *testing.T) {
	var seen *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		body, _ = readRequestBody(r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	line := `curl -sS -X POST ` + srv.URL + `/v1/broker-access ` +
		`-H "Authorization: Bearer TOKEN" -H "Content-Type: application/json" ` +
		`-d '{"broker":"tinkoff","token":"SANDBOX_TOKEN"}'`
	req, _ := mustCurl(t, line)

	// Exactly what capability.SendInputs and the send handler do, minus the
	// transport: no environment, so no lookup but the request's own, and no
	// binding store behind Apply.
	own, err := apicoll.RequestLookup(req, apicoll.Environment{})
	if err != nil {
		t.Fatalf("RequestLookup: %v", err)
	}
	resolved, err := apicoll.Substitute(req, own)
	if err != nil {
		t.Fatalf("Substitute refused the imported request: %v", err)
	}
	sending, used, err := apisend.Apply(resolved, apisend.SecretSource{})
	if err != nil {
		t.Fatalf("Apply refused the imported request: %v", err)
	}

	ex, err := apisend.New().Send(t.Context(), sending, apisend.Key{CookieScope: "test"}, used...)
	if err != nil {
		t.Fatalf("Send refused the imported request: %v", err)
	}
	if ex.Failure != nil {
		t.Fatalf("failure = %+v", ex.Failure)
	}
	if seen == nil {
		t.Fatal("the server saw no request: the send never left compose")
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer TOKEN" {
		t.Fatalf("Authorization on the wire = %q, want the line's", got)
	}
	if got := seen.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type on the wire = %q", got)
	}
	if string(body) != `{"broker":"tinkoff","token":"SANDBOX_TOKEN"}` {
		t.Fatalf("body on the wire = %q", body)
	}
	if seen.Method != "POST" {
		t.Fatalf("method on the wire = %q", seen.Method)
	}
}

// The paired refusal: a line naming a variable NOBODY bound is still
// refused at compose, so the fix above did not turn every unresolved
// reference into a silent empty value.
func TestCurlLineNamingAnUnboundVariableIsStillRefused(t *testing.T) {
	req, _ := mustCurl(t, `curl -H 'Authorization: Bearer {{tok}}' https://api.example/x`)
	own, err := apicoll.RequestLookup(req, apicoll.Environment{})
	if err != nil {
		t.Fatalf("RequestLookup: %v", err)
	}
	if _, err := apicoll.Substitute(req, own); err == nil {
		t.Fatal("a reference to an unbound variable substituted cleanly")
	}
}

// readRequestBody is io.ReadAll over the request's own body, kept here so
// the test file names one import fewer.
func readRequestBody(r *http.Request) ([]byte, error) {
	defer func() { _ = r.Body.Close() }()
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}
