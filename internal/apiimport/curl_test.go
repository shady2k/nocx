package apiimport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

// mustCurl parses and fails the test on error.
func mustCurl(t *testing.T, line string) (apicoll.Request, []Unsupported) {
	t.Helper()
	req, unsup, err := FromCurl(line)
	if err != nil {
		t.Fatalf("FromCurl(%q) error: %v", line, err)
	}
	return req, unsup
}

// assertAbsent marshals the whole request and fails if needle appears
// anywhere in it. The rule is "never written into the request", so the
// assertion is over the whole value rather than over the field we happen to
// suspect.
func assertAbsent(t *testing.T, v any, needle string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), needle) {
		t.Fatalf("found %q in %s", needle, raw)
	}
}

func unsupportedWhat(unsup []Unsupported) []string {
	out := make([]string, 0, len(unsup))
	for _, u := range unsup {
		out = append(out, u.What)
	}
	return out
}

func hasUnsupported(unsup []Unsupported, what string) bool {
	for _, u := range unsup {
		if u.What == what {
			return true
		}
	}
	return false
}

func headerValue(req apicoll.Request, name string) (string, bool) {
	for _, h := range req.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value, true
		}
	}
	return "", false
}

func queryValue(req apicoll.Request, name string) (string, bool) {
	for _, p := range req.Query {
		if p.Name == name {
			return p.Value, true
		}
	}
	return "", false
}

// ---- one test per supported flag (design §10) ----

func TestCurlFlag_X_Request(t *testing.T) {
	for _, line := range []string{
		`curl -X POST https://api.example/users`,
		`curl -XPOST https://api.example/users`,
		`curl --request POST https://api.example/users`,
		`curl --request=POST https://api.example/users`,
	} {
		req, unsup := mustCurl(t, line)
		if req.Method != "POST" {
			t.Fatalf("%s: method = %q, want POST", line, req.Method)
		}
		if len(unsup) != 0 {
			t.Fatalf("%s: unsupported = %v", line, unsupportedWhat(unsup))
		}
	}
	// Lower case is normalised, because a method is a token and the model
	// holds one spelling of it.
	req, _ := mustCurl(t, `curl -X delete https://api.example/users/1`)
	if req.Method != "DELETE" {
		t.Fatalf("method = %q, want DELETE", req.Method)
	}
}

func TestCurlFlag_H_Header(t *testing.T) {
	req, unsup := mustCurl(t, `curl -H 'X-Trace: abc' --header 'Accept: application/json' https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if v, ok := headerValue(req, "X-Trace"); !ok || v != "abc" {
		t.Fatalf("X-Trace = %q %v", v, ok)
	}
	if v, ok := headerValue(req, "Accept"); !ok || v != "application/json" {
		t.Fatalf("Accept = %q %v", v, ok)
	}
	for _, h := range req.Headers {
		if !h.Enabled {
			t.Fatalf("header %q imported disabled", h.Name)
		}
	}
	// curl's "send this header with no value" form.
	req2, _ := mustCurl(t, `curl -H 'X-Empty;' https://api.example/x`)
	if v, ok := headerValue(req2, "X-Empty"); !ok || v != "" {
		t.Fatalf("X-Empty = %q %v", v, ok)
	}
}

func TestCurlFlag_d_Data(t *testing.T) {
	req, unsup := mustCurl(t, `curl -d '{"a":1}' https://api.example/users`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Method != "POST" {
		t.Fatalf("method = %q, want POST (a body implies POST)", req.Method)
	}
	if req.Body.Kind != apicoll.BodyRaw || req.Body.Text != `{"a":1}` {
		t.Fatalf("body = %+v", req.Body)
	}
	// Repeated -d joins with & — curl's own rule.
	req2, _ := mustCurl(t, `curl -d a=1 -d b=2 https://api.example/users`)
	if req2.Body.Text != "a=1&b=2" {
		t.Fatalf("joined body = %q", req2.Body.Text)
	}
	// -d @file names the file rather than reading it: an import touches no
	// file the input names.
	req3, _ := mustCurl(t, `curl -d @body.json https://api.example/users`)
	if req3.Body.Kind != apicoll.BodyFile || req3.Body.FileRef != "body.json" {
		t.Fatalf("file body = %+v", req3.Body)
	}
}

func TestCurlFlag_DataRaw(t *testing.T) {
	// The whole difference from -d: @ is NOT a file reference here.
	req, unsup := mustCurl(t, `curl --data-raw '@body.json' https://api.example/users`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Body.Kind != apicoll.BodyRaw || req.Body.Text != "@body.json" {
		t.Fatalf("body = %+v, want a raw literal", req.Body)
	}
}

func TestCurlFlag_DataBinary(t *testing.T) {
	req, unsup := mustCurl(t, `curl --data-binary @payload.bin https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Body.Kind != apicoll.BodyFile || req.Body.FileRef != "payload.bin" {
		t.Fatalf("body = %+v", req.Body)
	}
	req2, _ := mustCurl(t, `curl --data-binary 'raw text' https://api.example/x`)
	if req2.Body.Kind != apicoll.BodyRaw || req2.Body.Text != "raw text" {
		t.Fatalf("inline binary body = %+v", req2.Body)
	}
}

func TestCurlFlag_DataUrlencode(t *testing.T) {
	req, unsup := mustCurl(t, `curl --data-urlencode 'q=a b&c' --data-urlencode 'n=1' https://api.example/s`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Body.Kind != apicoll.BodyForm {
		t.Fatalf("body kind = %q, want form", req.Body.Kind)
	}
	if req.Body.Text != "q=a+b%26c&n=1" {
		t.Fatalf("body = %q", req.Body.Text)
	}
	// The @file forms are refused rather than read.
	_, unsup2 := mustCurl(t, `curl --data-urlencode 'q@file.txt' https://api.example/s`)
	if !hasUnsupported(unsup2, "--data-urlencode name@file") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup2))
	}
}

func TestCurlFlag_F_Form(t *testing.T) {
	req, unsup := mustCurl(t, `curl -F name=alice -F 'note=hello world' https://api.example/u`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Method != "POST" {
		t.Fatalf("method = %q, want POST", req.Method)
	}
	if req.Body.Kind != apicoll.BodyForm || req.Body.Text != "name=alice&note=hello+world" {
		t.Fatalf("body = %+v", req.Body)
	}
	// A file part changes what goes on the wire and cannot be a text pair,
	// so it is itemised rather than dropped.
	_, unsup2 := mustCurl(t, `curl -F 'file=@photo.png' https://api.example/u`)
	if !hasUnsupported(unsup2, "-F file part") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup2))
	}
	// Multipart is converted to urlencoded, which is a different
	// Content-Type: said out loud.
	_, unsup3 := mustCurl(t, `curl -F name=alice https://api.example/u`)
	_ = unsup3
	if !hasUnsupported(mustUnsup(t, `curl -F name=alice -F 'x=@f.bin' https://api.example/u`), "-F file part") {
		t.Fatal("mixed form did not itemise its file part")
	}
}

func mustUnsup(t *testing.T, line string) []Unsupported {
	t.Helper()
	_, u := mustCurl(t, line)
	return u
}

func TestCurlFlag_Json(t *testing.T) {
	req, unsup := mustCurl(t, `curl --json '{"a":1}' https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Body.Kind != apicoll.BodyRaw || req.Body.Text != `{"a":1}` {
		t.Fatalf("body = %+v", req.Body)
	}
	// curl's --json implies both headers; dropping them changes what the
	// server does with the body.
	if v, ok := headerValue(req, "Content-Type"); !ok || v != "application/json" {
		t.Fatalf("Content-Type = %q %v", v, ok)
	}
	if v, ok := headerValue(req, "Accept"); !ok || v != "application/json" {
		t.Fatalf("Accept = %q %v", v, ok)
	}
	// An explicit -H wins: curl does not add a header the user already set.
	req2, _ := mustCurl(t, `curl --json '{}' -H 'Content-Type: application/vnd.x+json' https://api.example/x`)
	if v, _ := headerValue(req2, "Content-Type"); v != "application/vnd.x+json" {
		t.Fatalf("explicit Content-Type overwritten: %q", v)
	}
}

func TestCurlFlag_u_User(t *testing.T) {
	req, unsup := mustCurl(t, `curl -u alice:s3cr3t-p4ssw0rd https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Auth.Kind != apicoll.AuthBasic {
		t.Fatalf("auth kind = %q", req.Auth.Kind)
	}
	if req.Auth.User != "alice" {
		t.Fatalf("auth user = %q", req.Auth.User)
	}
	if req.Auth.Var == "" {
		t.Fatal("auth names no variable")
	}
	assertAbsent(t, req, "s3cr3t-p4ssw0rd")

	// A password containing a colon splits at the FIRST one.
	req2, _ := mustCurl(t, `curl -u alice:a:b https://api.example/x`)
	if req2.Auth.User != "alice" {
		t.Fatalf("auth user = %q", req2.Auth.User)
	}
	assertAbsent(t, req2, "a:b")

	// No colon: curl would prompt. The variable is named and unbound, so
	// the send blocks and says which variable is missing (§6.5).
	req3, _ := mustCurl(t, `curl -u alice https://api.example/x`)
	if req3.Auth.Kind != apicoll.AuthBasic || req3.Auth.User != "alice" || req3.Auth.Var == "" {
		t.Fatalf("auth = %+v", req3.Auth)
	}
}

func TestCurlFlag_b_Cookie(t *testing.T) {
	req, unsup := mustCurl(t, `curl -b 'sid=1; theme=dark' https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if v, ok := headerValue(req, "Cookie"); !ok || v != "sid=1; theme=dark" {
		t.Fatalf("Cookie = %q %v", v, ok)
	}
	// The other -b: a cookie JAR file, which is a file we will not read.
	_, unsup2 := mustCurl(t, `curl -b cookies.txt https://api.example/x`)
	if !hasUnsupported(unsup2, "-b cookie file") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup2))
	}
}

func TestCurlFlag_G_Get(t *testing.T) {
	req, unsup := mustCurl(t, `curl -G -d 'q=hello' -d 'n=2' https://api.example/search`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Method != "GET" {
		t.Fatalf("method = %q, want GET", req.Method)
	}
	if req.Body.Kind != apicoll.BodyNone {
		t.Fatalf("body = %+v, want none: -G moves the data to the query", req.Body)
	}
	if v, ok := queryValue(req, "q"); !ok || v != "hello" {
		t.Fatalf("q = %q %v", v, ok)
	}
	if v, ok := queryValue(req, "n"); !ok || v != "2" {
		t.Fatalf("n = %q %v", v, ok)
	}
	// -G with an explicit -X keeps the explicit method: curl does.
	req2, _ := mustCurl(t, `curl -G -X HEAD -d 'q=1' https://api.example/search`)
	if req2.Method != "HEAD" {
		t.Fatalf("method = %q, want HEAD", req2.Method)
	}
}

func TestCurlFlag_L_Location(t *testing.T) {
	req, unsup := mustCurl(t, `curl -L https://api.example/x`)
	if req.URL != "https://api.example/x" {
		t.Fatalf("url = %q", req.URL)
	}
	if !hasUnsupported(unsup, "--location") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	_, unsup2 := mustCurl(t, `curl --location https://api.example/x`)
	if !hasUnsupported(unsup2, "--location") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup2))
	}
}

func TestCurlFlag_k_Insecure(t *testing.T) {
	// -k turns off certificate verification. Dropping it silently produces
	// a request that fails a handshake for a reason nothing explains.
	_, unsup := mustCurl(t, `curl -k https://api.example/x`)
	if !hasUnsupported(unsup, "--insecure") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	_, unsup2 := mustCurl(t, `curl --insecure https://api.example/x`)
	if !hasUnsupported(unsup2, "--insecure") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup2))
	}
}

func TestCurlFlag_Compressed(t *testing.T) {
	_, unsup := mustCurl(t, `curl --compressed https://api.example/x`)
	if !hasUnsupported(unsup, "--compressed") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
}

// Bundled short flags are how these lines are actually written.
func TestCurlBundledShortFlags(t *testing.T) {
	req, unsup := mustCurl(t, `curl -skL https://api.example/x`)
	if req.URL != "https://api.example/x" {
		t.Fatalf("url = %q", req.URL)
	}
	if !hasUnsupported(unsup, "--insecure") || !hasUnsupported(unsup, "--location") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	// A bundle may end in a value-taking flag.
	req2, _ := mustCurl(t, `curl -sXPUT https://api.example/x`)
	if req2.Method != "PUT" {
		t.Fatalf("method = %q", req2.Method)
	}
}

// ---- refused out loud (design §10) ----

func TestCurlRefusedFlags(t *testing.T) {
	cases := []struct{ line, what string }{
		{`curl --proxy http://127.0.0.1:8080 https://api.example/x`, "--proxy"},
		{`curl -x http://127.0.0.1:8080 https://api.example/x`, "--proxy"},
		{`curl --cert client.pem https://api.example/x`, "--cert"},
		{`curl -E client.pem https://api.example/x`, "--cert"},
		{`curl -o out.json https://api.example/x`, "--output"},
		{`curl --output out.json https://api.example/x`, "--output"},
	}
	for _, tc := range cases {
		req, unsup := mustCurl(t, tc.line)
		if !hasUnsupported(unsup, tc.what) {
			t.Fatalf("%s: unsupported = %v, want %s", tc.line, unsupportedWhat(unsup), tc.what)
		}
		// The flag's ARGUMENT is not a URL, and must not have been taken
		// for one.
		if req.URL != "https://api.example/x" {
			t.Fatalf("%s: url = %q — the refused flag's argument was eaten as a URL", tc.line, req.URL)
		}
	}
}

func TestCurlUnknownFlagIsItemised(t *testing.T) {
	_, unsup := mustCurl(t, `curl --hypothetical-future-flag https://api.example/x`)
	if !hasUnsupported(unsup, "--hypothetical-future-flag") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	_, unsup2 := mustCurl(t, `curl -Z https://api.example/x`)
	if !hasUnsupported(unsup2, "-Z") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup2))
	}
}

// A refused flag's argument may be a credential. Itemising it would put the
// credential in the report that is shown to the user and logged.
func TestCurlRefusedFlagNeverEchoesItsArgument(t *testing.T) {
	const token = "sk-live-0123456789abcdefghij"
	_, unsup := mustCurl(t, `curl --oauth2-bearer `+token+` https://api.example/x`)
	if len(unsup) == 0 {
		t.Fatal("--oauth2-bearer was not itemised")
	}
	for _, u := range unsup {
		if strings.Contains(u.What, token) || strings.Contains(u.Why, token) {
			t.Fatalf("the refusal echoed the credential: %+v", u)
		}
	}
}

// ---- the Authorization rule (§8) ----

func TestCurlAuthorizationBearerBecomesAVariable(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlLXZhbHVl" //nolint:gosec // a synthetic token: the test exists to prove this exact string reaches no file
	req, unsup := mustCurl(t, `curl -H 'Authorization: Bearer `+token+`' https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Auth.Kind != apicoll.AuthBearer {
		t.Fatalf("auth kind = %q, want bearer", req.Auth.Kind)
	}
	if req.Auth.Var == "" {
		t.Fatal("auth names no variable")
	}
	if _, ok := headerValue(req, "Authorization"); ok {
		t.Fatal("the Authorization header survived into the request")
	}
	assertAbsent(t, req, token)
}

func TestCurlAuthorizationBasicBecomesAVariable(t *testing.T) {
	// base64("alice:s3cr3t-p4ssw0rd")
	const enc = "YWxpY2U6czNjcjN0LXA0c3N3MHJk"
	req, _ := mustCurl(t, `curl -H 'Authorization: Basic `+enc+`' https://api.example/x`)
	if req.Auth.Kind != apicoll.AuthBasic || req.Auth.User != "alice" || req.Auth.Var == "" {
		t.Fatalf("auth = %+v", req.Auth)
	}
	assertAbsent(t, req, "s3cr3t-p4ssw0rd")
	assertAbsent(t, req, enc)
}

func TestCurlAuthorizationUnknownSchemeIsRefusedNotWritten(t *testing.T) {
	const cred = "0123456789abcdefghij"
	req, unsup := mustCurl(t, `curl -H 'Authorization: Digest `+cred+`' https://api.example/x`)
	if !hasUnsupported(unsup, "Authorization: Digest") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	assertAbsent(t, req, cred)
}

// A value that is ALREADY one of our variable references is not a secret:
// binding it would store the literal text "{{tok}}" as a credential.
func TestCurlAuthorizationAlreadyAVariableIsCarriedThrough(t *testing.T) {
	req, unsup := mustCurl(t, `curl -H 'Authorization: Bearer {{tok}}' https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Auth.Kind != apicoll.AuthBearer || req.Auth.Var != "tok" {
		t.Fatalf("auth = %+v, want the existing variable name", req.Auth)
	}
}

func TestCurlSecretShapedHeaderBecomesAVariable(t *testing.T) {
	const key = "sk-abcdefghijklmnopqrstuvwxyz0123"
	req, _ := mustCurl(t, `curl -H 'X-API-Key: `+key+`' https://api.example/x`)
	assertAbsent(t, req, key)
	v, ok := headerValue(req, "X-API-Key")
	if !ok {
		t.Fatal("the header was dropped rather than made a variable")
	}
	if !strings.HasPrefix(v, "{{") || !strings.HasSuffix(v, "}}") {
		t.Fatalf("X-API-Key = %q, want a {{variable}}", v)
	}
}

// The paired negative: an ordinary header is left exactly alone. A false
// positive here turns a working request into an unresolved variable.
func TestCurlOrdinaryHeadersAreNotTreatedAsSecrets(t *testing.T) {
	req, _ := mustCurl(t, `curl -H 'Content-Type: application/json' -H 'Accept-Language: en-GB' -H 'X-Request-Id: 42' https://api.example/x`)
	for _, name := range []string{"Content-Type", "Accept-Language", "X-Request-Id"} {
		v, ok := headerValue(req, name)
		if !ok {
			t.Fatalf("%s was dropped", name)
		}
		if strings.Contains(v, "{{") {
			t.Fatalf("%s = %q — an ordinary header was turned into a variable", name, v)
		}
	}
}

// ---- URL, query and templates ----

func TestCurlSplitsTheQueryOffTheURL(t *testing.T) {
	req, _ := mustCurl(t, `curl 'https://api.example/search?q=a%20b&n=2'`)
	if req.URL != "https://api.example/search" {
		t.Fatalf("url = %q", req.URL)
	}
	if v, ok := queryValue(req, "q"); !ok || v != "a b" {
		t.Fatalf("q = %q %v", v, ok)
	}
	if v, ok := queryValue(req, "n"); !ok || v != "2" {
		t.Fatalf("n = %q %v", v, ok)
	}
}

func TestCurlTemplatesSurvive(t *testing.T) {
	req, _ := mustCurl(t, `curl '{{baseUrl}}/users?tenant={{tenant}}' -H 'X-Env: {{env}}'`)
	if req.URL != "{{baseUrl}}/users" {
		t.Fatalf("url = %q, want the template intact", req.URL)
	}
	if v, _ := queryValue(req, "tenant"); v != "{{tenant}}" {
		t.Fatalf("tenant = %q", v)
	}
	if v, _ := headerValue(req, "X-Env"); v != "{{env}}" {
		t.Fatalf("X-Env = %q", v)
	}
}

func TestCurlExtraURLsAreItemised(t *testing.T) {
	req, unsup := mustCurl(t, `curl https://a.example/x https://b.example/y`)
	if req.URL != "https://a.example/x" {
		t.Fatalf("url = %q", req.URL)
	}
	if !hasUnsupported(unsup, "second URL") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
}

// ---- failures, each paired with the ordinary line that succeeds ----

func TestCurlErrors(t *testing.T) {
	cases := []struct{ name, line string }{
		{"empty", ``},
		{"whitespace only", "  \n  "},
		{"not curl", `wget https://api.example/x`},
		{"curl with no URL", `curl -X POST`},
		{"unterminated quote", `curl -H 'A: b https://api.example/x`},
		{"flag wants a value it has not got", `curl https://api.example/x -H`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := FromCurl(tc.line); err == nil {
				t.Fatalf("FromCurl(%q) succeeded, want an error", tc.line)
			}
		})
	}
	// And on an ordinary line it succeeds.
	req, _ := mustCurl(t, `curl https://api.example/x`)
	if req.Method != "GET" || req.URL != "https://api.example/x" {
		t.Fatalf("req = %+v", req)
	}
}

func TestCurlAcceptsAShellPromptAndAPathToCurl(t *testing.T) {
	for _, line := range []string{
		`$ curl https://api.example/x`,
		`/usr/bin/curl https://api.example/x`,
		`CURL https://api.example/x`,
	} {
		req, _ := mustCurl(t, line)
		if req.URL != "https://api.example/x" {
			t.Fatalf("%s: url = %q", line, req.URL)
		}
	}
}

// The shell constructs reach the model as literal text. Absence of the exec
// is asserted separately (TestPackageNeverExecs); this is the other half —
// the bytes are carried, not interpreted and not stripped.
func TestCurlShellConstructsAreLiteralText(t *testing.T) {
	req, _ := mustCurl(t, `curl -d '$(touch /tmp/pwned)' -H 'X-A: `+"`id`"+`' 'https://api.example/x'`)
	if req.Body.Text != "$(touch /tmp/pwned)" {
		t.Fatalf("body = %q, want the literal text", req.Body.Text)
	}
	if v, _ := headerValue(req, "X-A"); v != "`id`" {
		t.Fatalf("X-A = %q, want the literal text", v)
	}
}

func TestCurlNameIsDerivedFromTheURL(t *testing.T) {
	req, _ := mustCurl(t, `curl -X POST https://api.example/v1/users`)
	if req.Name == "" {
		t.Fatal("request has no name")
	}
	if !strings.Contains(req.Name, "users") {
		t.Fatalf("name = %q, want something derived from the path", req.Name)
	}
}
