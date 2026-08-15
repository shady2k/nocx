package profile

// The endpoint's custom HTTP headers (bead nocx-lyyk): names and literal
// values are validated at the record gate, because a stored record must
// never hold a header nobody can send — the refused set, the control
// characters and the duplicate rule are all decided here, and the wire
// (transport) reuses this same predicate rather than owning a second copy.

import (
	"strings"
	"testing"
)

func TestHasControlChars(t *testing.T) {
	cases := map[string]bool{
		"":                 false,
		"plain":            false,
		"with space":       false,
		"tab\tinside":      true,
		"newline\ninside":  true,
		"carriage\rreturn": true,
		"escape\x1b":       true,
		"del\x7f":          true,
		"c1\x85control":    true,
		"utf8 ünïcödé":     false,
	}
	for in, want := range cases {
		if got := HasControlChars(in); got != want {
			t.Errorf("HasControlChars(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestValidateEndpointHeaderName(t *testing.T) {
	valid := []string{
		"X-Title",
		"HTTP-Referer",
		"api-key",
		"X-Tenant-Id",
		"x-custom_thing.1",
	}
	for _, name := range valid {
		if err := ValidateEndpointHeaderName(name); err != nil {
			t.Errorf("ValidateEndpointHeaderName(%q) = %v, want nil", name, err)
		}
	}

	invalid := map[string]string{
		"":                    "required",
		"X-Tab\tName":         "control",
		"X-Newline\nName":     "control",
		"X-Name\x7f":          "control",
		"Bad Name":            "not a valid HTTP field name",
		"Bad,Name":            "not a valid HTTP field name",
		"Bad(Name)":           "not a valid HTTP field name",
		"Authorization":       "refused",
		"authorization":       "refused",
		"Host":                "refused",
		"Content-Length":      "refused",
		"Content-Type":        "refused",
		"Connection":          "refused",
		"Keep-Alive":          "refused",
		"Proxy-Authorization": "refused",
		"TE":                  "refused",
		"Trailer":             "refused",
		"Transfer-Encoding":   "refused",
		"Upgrade":             "refused",
	}
	for name, reason := range invalid {
		err := ValidateEndpointHeaderName(name)
		if err == nil {
			t.Errorf("ValidateEndpointHeaderName(%q) = nil, want a %s error", name, reason)
			continue
		}
		if reason == "refused" && !strings.Contains(err.Error(), "refused") {
			t.Errorf("ValidateEndpointHeaderName(%q) = %q, want a refusal naming the reason", name, err)
		}
	}
}

func TestValidateEndpointHeaders(t *testing.T) {
	literal := "nocx"
	headers := []EndpointHeader{{Name: "HTTP-Referer", Value: &literal}}
	if err := ValidateEndpointHeaders(headers); err != nil {
		t.Fatalf("valid headers refused: %v", err)
	}

	cases := map[string][]EndpointHeader{
		"neither source": {
			{Name: "X-Title"},
		},
		"both sources": {
			{Name: "X-Title", Value: &literal, ValueRef: "sec:v1:test:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		"control character in literal": {
			{Name: "X-Title", Value: ptr("line\nbreak")},
		},
		"duplicate names case-insensitively": {
			{Name: "X-Title", Value: &literal},
			{Name: "x-title", Value: ptr("second")},
		},
		"refused name": {
			{Name: "Authorization", Value: &literal},
		},
		"control character in name": {
			{Name: "X-Bad\x01Name", Value: &literal},
		},
	}
	for name, hs := range cases {
		if err := ValidateEndpointHeaders(hs); err == nil {
			t.Errorf("ValidateEndpointHeaders(%s) = nil, want a refusal", name)
		}
	}
}

func TestValidateEndpoint_RejectsBadHeaders(t *testing.T) {
	e := validTestEndpoint()
	bad := "Bad Value\n"
	e.Headers = []EndpointHeader{{Name: "X-Title", Value: &bad}}
	if err := ValidateEndpoint(e); err == nil {
		t.Fatal("ValidateEndpoint accepted a control character in a header value")
	}
	e = validTestEndpoint()
	e.Headers = []EndpointHeader{{Name: "Host", Value: ptr("evil.example")}}
	if err := ValidateEndpoint(e); err == nil {
		t.Fatal("ValidateEndpoint accepted a refused header name")
	}
}
