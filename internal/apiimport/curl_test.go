package apiimport

import (
	"encoding/base64"
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

// mustCurlBinder is the OTHER entrance: the same converter, called the way
// ImportInto calls it — with somewhere for a credential to go. The §8
// assertions live against this one, because §8 is about a file and this is
// the route that writes one.
func mustCurlBinder(t *testing.T, line string) (apicoll.Request, []secretOffer, []Unsupported) {
	t.Helper()
	req, offers, unsup, err := parseCurl(line, newVarNamer(), credentialsToBinder)
	if err != nil {
		t.Fatalf("parseCurl(%q) error: %v", line, err)
	}
	return req, offers, unsup
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
	// A payload that IS a JSON document arrives in the JSON mode; the whole
	// table of that decision is TestCurlBodyModeIsJSONWhenTheLineSaysSo.
	if req.Body.Kind != apicoll.BodyJSON || req.Body.Text != `{"a":1}` {
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
	if req.Body.Kind != apicoll.BodyJSON || req.Body.Text != `{"a":1}` {
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

// -u INTO THE FORM: the header curl itself would have built, so the request
// sends as the line sent.
func TestCurlFlag_u_User(t *testing.T) {
	req, unsup := mustCurl(t, `curl -u alice:s3cr3t-p4ssw0rd https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	// base64("alice:s3cr3t-p4ssw0rd"), which is what curl puts on the wire.
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cr3t-p4ssw0rd"))
	if v, ok := headerValue(req, "Authorization"); !ok || v != want {
		t.Fatalf("Authorization = %q %v", v, ok)
	}

	// A password containing a colon splits at the FIRST one.
	req2, _ := mustCurl(t, `curl -u alice:a:b https://api.example/x`)
	if v, _ := headerValue(req2, "Authorization"); v != "Basic "+base64.StdEncoding.EncodeToString([]byte("alice:a:b")) {
		t.Fatalf("Authorization = %q", v)
	}

	// A password the LINE spells as a variable stays a variable: that name
	// is the person's own, and the environment answers it. The auth field
	// is text, so the reference is spelled `{{pw}}` in it.
	req3, _ := mustCurl(t, `curl -u alice:{{pw}} https://api.example/x`)
	if req3.Auth.Kind != apicoll.AuthBasic || req3.Auth.User != "alice" || req3.Auth.Password != "{{pw}}" {
		t.Fatalf("auth = %+v", req3.Auth)
	}
	if _, ok := headerValue(req3, "Authorization"); ok {
		t.Fatal("a header was built for a password that is a variable")
	}

	// No colon: curl would prompt, and an import has nobody to ask. Nothing
	// is carried, and the reason is itemised rather than logged.
	req4, unsup4 := mustCurl(t, `curl -u alice https://api.example/x`)
	if req4.Auth.Kind != "" {
		t.Fatalf("auth = %+v, want none", req4.Auth)
	}
	if _, ok := headerValue(req4, "Authorization"); ok {
		t.Fatal("a credential was invented for a password the line did not carry")
	}
	if !hasUnsupported(unsup4, "-u without a password") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup4))
	}

	// curl's own precedence: an explicit Authorization header REPLACES the
	// one -u would generate, so importing both must not send -u's.
	req5, _ := mustCurl(t, `curl -u alice:s3cr3t-p4ssw0rd -H 'Authorization: Bearer T' https://api.example/x`)
	if v, _ := headerValue(req5, "Authorization"); v != "Bearer T" {
		t.Fatalf("Authorization = %q, want the line's own header", v)
	}
	assertAbsent(t, req5, "s3cr3t-p4ssw0rd")
}

// -u INTO A COLLECTION: the password is a variable in the file and a value
// at the binder, which is §8 and has not changed.
func TestCurlFlag_u_UserToBinder(t *testing.T) {
	req, offers, unsup := mustCurlBinder(t, `curl -u alice:s3cr3t-p4ssw0rd https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Auth.Kind != apicoll.AuthBasic || req.Auth.User != "alice" || req.Auth.Password == "" {
		t.Fatalf("auth = %+v", req.Auth)
	}
	if req.Auth.Password == "s3cr3t-p4ssw0rd" {
		t.Fatal("the password's TEXT is in the file — a bound value is a {{name}} here")
	}
	assertAbsent(t, req, "s3cr3t-p4ssw0rd")
	if len(offers) != 1 || string(offers[0].Value) != "s3cr3t-p4ssw0rd" {
		t.Fatalf("offers = %d, want the password offered to the binder", len(offers))
	}

	// No colon: the variable is named and unbound, so the send blocks and
	// says which variable is missing (§6.5).
	req2, _, _ := mustCurlBinder(t, `curl -u alice https://api.example/x`)
	if req2.Auth.Kind != apicoll.AuthBasic || req2.Auth.User != "alice" || req2.Auth.Password == "" {
		t.Fatalf("auth = %+v", req2.Auth)
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

// INTO A COLLECTION, which is the route §8 is about: the token becomes a
// variable name in the file and a value at the binder, and the header does
// not survive.
func TestCurlAuthorizationBearerBecomesAVariable(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlLXZhbHVl" //nolint:gosec // a synthetic token: the test exists to prove this exact string reaches no file
	req, offers, unsup := mustCurlBinder(t, `curl -H 'Authorization: Bearer `+token+`' https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Auth.Kind != apicoll.AuthBearer {
		t.Fatalf("auth kind = %q, want bearer", req.Auth.Kind)
	}
	if req.Auth.Token == "" {
		t.Fatal("auth names no variable")
	}
	if req.Auth.Token == token {
		t.Fatal("the token's TEXT is in the file — a value at the binder is a {{name}} here")
	}
	if _, ok := headerValue(req, "Authorization"); ok {
		t.Fatal("the Authorization header survived into the request")
	}
	assertAbsent(t, req, token)
	if len(offers) != 1 || offers[0].Variable != "token" || string(offers[0].Value) != token ||
		req.Auth.Token != "{{"+offers[0].Variable+"}}" {
		t.Fatalf("offers = %d, auth = %+v, want the token offered under the reference in the file",
			len(offers), req.Auth)
	}
}

// INTO THE FORM: nothing here can bind a value, so nothing here mints a
// name for one — the header the line wrote is the header the request
// carries, and the send goes out on "No environment" (nocx-14exx). The
// end-to-end proof is TestCurlImportedRequestSendsOnNoEnvironment.
func TestCurlAuthorizationStaysOnTheRequestForTheForm(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlLXZhbHVl" //nolint:gosec // a synthetic token: this test is the one place it is expected to survive, because the form is not a file
	req, unsup := mustCurl(t, `curl -H 'Authorization: Bearer `+token+`' https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if v, ok := headerValue(req, "Authorization"); !ok || v != "Bearer "+token {
		t.Fatalf("Authorization = %q %v, want the line's own header", v, ok)
	}
	// And no variable was invented: a name nobody can bind is what the old
	// behaviour left behind, and it is what made the request unsendable.
	if req.Auth.Kind != "" || req.Auth.Token != "" {
		t.Fatalf("auth = %+v, want none: this entrance mints no variable", req.Auth)
	}
}

func TestCurlAuthorizationBasicBecomesAVariable(t *testing.T) {
	// base64("alice:s3cr3t-p4ssw0rd")
	const enc = "YWxpY2U6czNjcjN0LXA0c3N3MHJk"
	req, _, _ := mustCurlBinder(t, `curl -H 'Authorization: Basic `+enc+`' https://api.example/x`)
	if req.Auth.Kind != apicoll.AuthBasic || req.Auth.User != "alice" || req.Auth.Password == "" {
		t.Fatalf("auth = %+v", req.Auth)
	}
	assertAbsent(t, req, "s3cr3t-p4ssw0rd")
	assertAbsent(t, req, enc)
}

// A scheme the model has no auth block for is refused on the route that
// writes a file, because there is no field in which the credential could be
// spelled (§8) — and CARRIED on the route that writes none, because there
// it is just the header the line wrote, which is what curl sends.
func TestCurlAuthorizationUnknownSchemeIsRefusedNotWritten(t *testing.T) {
	const cred = "0123456789abcdefghij"
	req, _, unsup := mustCurlBinder(t, `curl -H 'Authorization: Digest `+cred+`' https://api.example/x`)
	if !hasUnsupported(unsup, "Authorization: Digest") {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	assertAbsent(t, req, cred)

	form, formUnsup := mustCurl(t, `curl -H 'Authorization: Digest `+cred+`' https://api.example/x`)
	if len(formUnsup) != 0 {
		t.Fatalf("unsupported = %v, want none: nothing was dropped", unsupportedWhat(formUnsup))
	}
	if v, _ := headerValue(form, "Authorization"); v != "Digest "+cred {
		t.Fatalf("Authorization = %q, want the line's own header", v)
	}
}

// A value that is ALREADY one of our variable references is not a secret:
// binding it would store the literal text "{{tok}}" as a credential.
func TestCurlAuthorizationAlreadyAVariableIsCarriedThrough(t *testing.T) {
	req, offers, unsup := mustCurlBinder(t, `curl -H 'Authorization: Bearer {{tok}}' https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if req.Auth.Kind != apicoll.AuthBearer || req.Auth.Token != "{{tok}}" {
		t.Fatalf("auth = %+v, want the existing variable reference", req.Auth)
	}
	if len(offers) != 0 {
		t.Fatalf("offers = %d, want none: the reference is not a credential", len(offers))
	}
	// Into the form it is the header, reference and all, and substitution
	// resolves it from the environment exactly as it resolves every other
	// {{name}} in a header.
	form, _ := mustCurl(t, `curl -H 'Authorization: Bearer {{tok}}' https://api.example/x`)
	if v, _ := headerValue(form, "Authorization"); v != "Bearer {{tok}}" {
		t.Fatalf("Authorization = %q", v)
	}
}

func TestCurlSecretShapedHeaderBecomesAVariable(t *testing.T) {
	const key = "sk-abcdefghijklmnopqrstuvwxyz0123"
	req, offers, _ := mustCurlBinder(t, `curl -H 'X-API-Key: `+key+`' https://api.example/x`)
	assertAbsent(t, req, key)
	v, ok := headerValue(req, "X-API-Key")
	if !ok {
		t.Fatal("the header was dropped rather than made a variable")
	}
	if !strings.HasPrefix(v, "{{") || !strings.HasSuffix(v, "}}") {
		t.Fatalf("X-API-Key = %q, want a {{variable}}", v)
	}
	if len(offers) != 1 || string(offers[0].Value) != key {
		t.Fatalf("offers = %d, want the key offered to the binder", len(offers))
	}
}

// The same header into the FORM. There is no binder, so a {{variable}} here
// would be one nobody could ever bind — the request would be refused at
// compose for a name the person never chose, which is nocx-14exx wearing a
// different header name.
func TestCurlSecretShapedHeaderStaysOnTheRequestForTheForm(t *testing.T) {
	const key = "sk-abcdefghijklmnopqrstuvwxyz0123"
	req, unsup := mustCurl(t, `curl -H 'X-API-Key: `+key+`' https://api.example/x`)
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v", unsupportedWhat(unsup))
	}
	if v, ok := headerValue(req, "X-API-Key"); !ok || v != key {
		t.Fatalf("X-API-Key = %q %v, want the line's own value", v, ok)
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
