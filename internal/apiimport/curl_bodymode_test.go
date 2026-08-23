package apiimport

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

// The line the owner pasted, and the whole of what it should produce: its
// two headers in order, its body in the mode its own Content-Type names,
// and NOTHING in the unsupported list, because -s and -S cannot change the
// request that is sent.
func TestCurlOwnerLineArrivesAsTheCommandThatWasPasted(t *testing.T) {
	req, unsup := mustCurl(t, `curl -sS -X POST http://127.0.0.1:8080/v1/broker-access `+
		`-H "Authorization: Bearer TOKEN" -H "Content-Type: application/json" `+
		`-d '{"broker":"tinkoff","token":"SANDBOX_TOKEN"}'`)

	want := []string{
		"Authorization: Bearer TOKEN",
		"Content-Type: application/json",
	}
	if got := headerRowsOf(req); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("headers =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if req.Body.Kind != apicoll.BodyJSON {
		t.Fatalf("body kind = %q, want %q: the line's own Content-Type names it", req.Body.Kind, apicoll.BodyJSON)
	}
	if req.Body.Text != `{"broker":"tinkoff","token":"SANDBOX_TOKEN"}` {
		t.Fatalf("body text = %q", req.Body.Text)
	}
	if req.Method != "POST" {
		t.Fatalf("method = %q", req.Method)
	}
	if len(unsup) != 0 {
		t.Fatalf("unsupported = %v, want none", unsupportedWhat(unsup))
	}
}

// The body mode follows the Content-Type the line names, or the payload
// itself when it names none. The paired negatives are the point: a form
// body and a plainly-not-JSON body are left alone.
func TestCurlBodyModeIsJSONWhenTheLineSaysSo(t *testing.T) {
	cases := []struct {
		name, line, want string
	}{
		{"content-type header", `curl -H 'Content-Type: application/json' -d 'not json at all' https://api.example/x`, apicoll.BodyJSON},
		{"content-type with charset", `curl -H 'Content-Type: application/json; charset=utf-8' -d '{}' https://api.example/x`, apicoll.BodyJSON},
		{"a +json media type", `curl -H 'Content-Type: application/vnd.api+json' -d '{}' https://api.example/x`, apicoll.BodyJSON},
		{"payload that is a JSON object", `curl -d '{"a":1}' https://api.example/x`, apicoll.BodyJSON},
		{"payload that is a JSON array", `curl -d '[1,2]' https://api.example/x`, apicoll.BodyJSON},
		{"--json", `curl --json '{"a":1}' https://api.example/x`, apicoll.BodyJSON},
		{"a form payload", `curl -d 'a=1&b=2' https://api.example/x`, apicoll.BodyRaw},
		{"a bare number is not a document", `curl -d '42' https://api.example/x`, apicoll.BodyRaw},
		{"a broken object", `curl -d '{"a":' https://api.example/x`, apicoll.BodyRaw},
		{"a form Content-Type is not JSON", `curl -H 'Content-Type: application/x-www-form-urlencoded' -d 'a=1' https://api.example/x`, apicoll.BodyRaw},
		{"urlencoded pairs stay a form", `curl --data-urlencode 'q=a b' https://api.example/x`, apicoll.BodyForm},
		{"a file body stays a file", `curl -d @body.json https://api.example/x`, apicoll.BodyFile},
		{"no body at all", `curl https://api.example/x`, apicoll.BodyNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := mustCurl(t, tc.line)
			if req.Body.Kind != tc.want {
				t.Fatalf("body kind = %q, want %q", req.Body.Kind, tc.want)
			}
		})
	}
}

// nocx-q2cx5's first half: a flag that cannot change the request that is
// sent is not reported at all, and the paired positive is that a flag which
// CAN change it still is.
func TestCurlFlagsThatCannotChangeTheRequestAreNotItemised(t *testing.T) {
	for _, line := range []string{
		`curl -sS https://api.example/x`,
		`curl -s -S -v -i -f -N -g --progress-bar https://api.example/x`,
		`curl -w '%{http_code}' https://api.example/x`,
		`curl --silent --show-error --verbose --include --fail https://api.example/x`,
	} {
		req, unsup := mustCurl(t, line)
		if len(unsup) != 0 {
			t.Fatalf("%s: unsupported = %v, want none", line, unsupportedWhat(unsup))
		}
		if req.URL != "https://api.example/x" {
			t.Fatalf("%s: url = %q — an ignored flag ate its neighbour", line, req.URL)
		}
	}
	// And the ones that CAN change it are still named out loud.
	for _, tc := range []struct{ line, what string }{
		{`curl -k https://api.example/x`, "--insecure"},
		{`curl -L https://api.example/x`, "--location"},
		{`curl -o out.json https://api.example/x`, "--output"},
	} {
		_, unsup := mustCurl(t, tc.line)
		if !hasUnsupported(unsup, tc.what) {
			t.Fatalf("%s: unsupported = %v, want %s", tc.line, unsupportedWhat(unsup), tc.what)
		}
	}
}
