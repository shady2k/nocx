package apicoll

import (
	"os"
	"reflect"
	"testing"
)

func symlink(t *testing.T, target, link string) error {
	t.Helper()
	return os.Symlink(target, link)
}

// §6.4's invariant, the half that is testable before the line projection
// exists: the file is the truth, so what comes back out of it is what went in.
func TestReadWriteRequest_RoundTrips(t *testing.T) {
	cases := []struct {
		name string
		rel  string
		req  Request
	}{
		{
			name: "empty headers and no body",
			rel:  "plain.json",
			req: Request{
				ID: "1", Name: "Plain", Method: "GET", URL: "http://x/",
				Body: Body{Kind: BodyNone}, Auth: Auth{Kind: AuthNone},
			},
		},
		{
			name: "a nil body kind, written back exactly as given",
			rel:  "nilbody.json",
			req:  Request{ID: "2", Name: "Nil body", Method: "HEAD", URL: "http://x/"},
		},
		{
			name: "a {{var}} in every field",
			rel:  "vars/templated.json",
			req: Request{
				ID: "{{id}}", Name: "{{name}}", Method: "{{method}}", URL: "{{baseUrl}}/{{path}}",
				Headers: []Header{{Name: "{{hname}}", Value: "{{hvalue}}", Enabled: true}},
				Query:   []Param{{Name: "{{qname}}", Value: "{{qvalue}}", Enabled: false}},
				Body:    Body{Kind: BodyRaw, Text: "{{payload}}"},
				Auth:    Auth{Kind: AuthBearer, Token: "{{tokenVar}}", User: "{{user}}"},
			},
		},
		{
			name: "a header value containing a colon",
			rel:  "colon.json",
			req: Request{
				ID: "4", Name: "Colon", Method: "GET", URL: "http://x/",
				Headers: []Header{
					{Name: "X-Time", Value: "12:34:56", Enabled: true},
					{Name: "Referer", Value: "https://example.com:8443/a", Enabled: true},
				},
				Body: Body{Kind: BodyNone}, Auth: Auth{Kind: AuthNone},
			},
		},
		{
			name: "a body with newlines",
			rel:  "newlines.json",
			req: Request{
				ID: "5", Name: "Newlines", Method: "POST", URL: "http://x/",
				Body: Body{Kind: BodyRaw, Text: "line one\nline two\r\n\ttabbed\n"},
				Auth: Auth{Kind: AuthNone},
			},
		},
		{
			name: "a file-named body",
			rel:  "deep/nested/file-body.json",
			req: Request{
				ID: "6", Name: "File body", Method: "PUT", URL: "http://x/",
				Body: Body{Kind: BodyFile, FileRef: "bodies/payload.bin"},
				Auth: Auth{Kind: AuthAPIKey, Token: "apiKey"},
			},
		},
		{
			name: "disabled rows are kept, not dropped",
			rel:  "disabled.json",
			req: Request{
				ID: "7", Name: "Disabled", Method: "GET", URL: "http://x/",
				Headers: []Header{{Name: "X-Off", Value: "kept", Enabled: false}},
				Query:   []Param{{Name: "page", Value: "2", Enabled: false}},
				Body:    Body{Kind: BodyNone}, Auth: Auth{Kind: AuthNone},
			},
		},
	}

	svc, h, _ := openTestCollection(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.WriteRequest(h, tc.rel, tc.req); err != nil {
				t.Fatalf("WriteRequest: %v", err)
			}
			got, err := svc.ReadRequest(h, tc.rel)
			if err != nil {
				t.Fatalf("ReadRequest: %v", err)
			}
			if !reflect.DeepEqual(got, tc.req) {
				t.Errorf("Read(Write(r)) = %#v, want %#v", got, tc.req)
			}
			// And the written request is the one that lists.
			coll, err := svc.List(h)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			var found bool
			for _, ref := range coll.Requests {
				if ref.RelPath == tc.rel {
					found = true
					if ref.Name != tc.req.Name || ref.Method != tc.req.Method {
						t.Errorf("listed %+v, want name %q method %q", ref, tc.req.Name, tc.req.Method)
					}
				}
			}
			if !found {
				t.Errorf("%q was written but does not list: %+v", tc.rel, coll.Requests)
			}
		})
	}
}

// An empty non-nil slice comes back nil: `omitempty` drops a zero-length
// slice and JSON has no way to keep the two apart. That is the ONE place
// Read(Write(r)) is not literally r, so it is named here rather than left for
// somebody to discover. Both ends stated: the FIRST write normalises, and
// every write after it is a fixed point.
func TestWriteRequest_EmptySlicesComeBackNilAndAreThenAFixedPoint(t *testing.T) {
	svc, h, _ := openTestCollection(t)
	given := Request{
		ID: "1", Name: "Empty", Method: "GET", URL: "http://x/",
		Headers: []Header{}, Query: []Param{},
		Body: Body{Kind: BodyNone}, Auth: Auth{Kind: AuthNone},
	}
	if err := svc.WriteRequest(h, "empty.json", given); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	first, err := svc.ReadRequest(h, "empty.json")
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if first.Headers != nil || first.Query != nil {
		t.Errorf("empty slices survived as %#v/%#v, want nil", first.Headers, first.Query)
	}
	if err = svc.WriteRequest(h, "empty.json", first); err != nil {
		t.Fatalf("second WriteRequest: %v", err)
	}
	second, err := svc.ReadRequest(h, "empty.json")
	if err != nil {
		t.Fatalf("second ReadRequest: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("the round trip is not a fixed point: %#v then %#v", first, second)
	}
	// And nothing else moved: only the two empty slices differ from what
	// was given.
	given.Headers, given.Query = nil, nil
	if !reflect.DeepEqual(first, given) {
		t.Errorf("Read(Write(r)) = %#v, want %#v with only the empty slices nil", first, given)
	}
}

// A write replaces the file rather than appending to it: a shorter request
// written over a longer one leaves no tail of the old JSON behind.
func TestWriteRequest_ReplacesTheFileWhole(t *testing.T) {
	svc, h, _ := openTestCollection(t)
	long := Request{
		ID: "1", Name: "Long", Method: "POST", URL: "http://x/",
		Headers: []Header{{Name: "A", Value: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Enabled: true}},
		Body:    Body{Kind: BodyRaw, Text: "a very long body indeed, repeated for length"},
		Auth:    Auth{Kind: AuthNone},
	}
	short := Request{
		ID: "1", Name: "Short", Method: "GET", URL: "http://x/",
		Body: Body{Kind: BodyNone}, Auth: Auth{Kind: AuthNone},
	}
	if err := svc.WriteRequest(h, "r.json", long); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := svc.WriteRequest(h, "r.json", short); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := svc.ReadRequest(h, "r.json")
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if !reflect.DeepEqual(got, short) {
		t.Errorf("after overwriting: %#v, want %#v", got, short)
	}
}
