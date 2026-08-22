package apisend

// A PLACED VALUE NEVER REACHES A MESSAGE (epic nocx-ew3uv).
//
// This file exists because of a measured leak, not a supposed one. Before a
// secret could be substituted into an address, `redact` cleared the userinfo
// and the query string — which is where a token most often rides a URL — and
// left the PATH alone. The moment .2 made a path segment a place a
// vault-held value can be, that became:
//
//	apisend: GET http://…/botsk-live-9f2c4e7a11b3d8/sendMessage?…: connect:
//	connection refused
//
// …in Failure.Reason, which crosses to the renderer and reaches any log that
// prints it. Telegram's is exactly that shape.
//
// So every message this package can produce about a request is asserted
// against the value, at each of the four places one can be substituted into
// and on each of the paths that build a message: the send error, the body
// read, and the redirect refusal.

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

const carried = "sk-live-9f2c4e7a11b3d8-and-then-some"

// deadAddress is an address nothing listens on — a listener opened and
// closed, so no port is guessed at.
func deadAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestRedaction_AFailureNeverCarriesAPlacedValue(t *testing.T) {
	addr := deadAddress(t)
	secret := NamedSecret{Name: "token", Value: carried}

	for name, req := range map[string]apicoll.Request{
		"in the path":  apicollGet("http://" + addr + "/bot" + carried + "/sendMessage"),
		"in the query": apicollGet("http://" + addr + "/x?t=" + carried),
		"in a header": {
			Method:  http.MethodGet,
			URL:     "http://" + addr + "/x",
			Headers: []apicoll.Header{{Name: "X-Key", Value: carried, Enabled: true}},
		},
		"in the body": {
			Method: http.MethodPost,
			URL:    "http://" + addr + "/x",
			Body:   apicoll.Body{Kind: apicoll.BodyRaw, Text: carried},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ex, err := New().Send(t.Context(), req, Key{}, secret)
			fail := failed(t, ex, err)
			if strings.Contains(fail.Reason, carried) {
				t.Errorf("the failure reason carries the value: %s", fail.Reason)
			}
			if strings.Contains(ex.Request.Text, carried) {
				t.Errorf("the request text carries the value: %s", ex.Request.Text)
			}
			// A partial leak is still a leak: a prefix long enough to be
			// recognisable must not survive either.
			if strings.Contains(fail.Reason, carried[:16]) {
				t.Errorf("the failure reason carries a prefix of the value: %s", fail.Reason)
			}
		})
	}
}

// THE PLACEHOLDER IS THERE, which is what makes the assertions above about
// ELISION rather than about a message that happens not to name the URL.
func TestRedaction_AnElidedURLNamesTheSecretWhereItWas(t *testing.T) {
	addr := deadAddress(t)
	ex, err := New().Send(t.Context(),
		apicollGet("http://"+addr+"/bot"+carried+"/sendMessage"),
		Key{}, NamedSecret{Name: "token", Value: carried})
	fail := failed(t, ex, err)
	if !strings.Contains(fail.Reason, "⟦token⟧") {
		t.Errorf("reason = %q, want the secret's place named in it", fail.Reason)
	}
	// The rest of the address survives, so the message still says where the
	// request was going.
	if !strings.Contains(fail.Reason, "/sendMessage") {
		t.Errorf("reason = %q, want the address still readable around the elision", fail.Reason)
	}
}

// A BODY THAT BREAKS MID-READ builds its own message with the URL in it —
// the second path to a message, and it must redact the same way.
func TestRedaction_ABodyReadFailureNeverCarriesAPlacedValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the test server does not support hijacking")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		writeAndClose(buf, "HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\npartial")
	}))
	defer srv.Close()

	ex, err := New().Send(t.Context(),
		apicollGet(srv.URL+"/bot"+carried+"/sendMessage"),
		Key{}, NamedSecret{Name: "token", Value: carried})
	fail := failedAt(t, ex, err, PhaseExchange)
	if strings.Contains(fail.Reason, carried) {
		t.Errorf("the body-read failure carries the value: %s", fail.Reason)
	}
	if !strings.Contains(fail.Reason, "⟦token⟧") {
		t.Errorf("reason = %q, want the secret's place named", fail.Reason)
	}
}

// AN ESCAPED VALUE IS ELIDED TOO. A value with characters a URL must encode
// arrives in the address percent-encoded, so a redactor looking only for the
// raw bytes would find nothing and leave the encoded credential standing.
func TestRedaction_AValueTheURLEncodedIsStillElided(t *testing.T) {
	addr := deadAddress(t)
	// Slashes and a plus: url.URL.String() re-encodes these in a query.
	const awkward = "a+b/c d=e"
	ex, err := New().Send(t.Context(),
		apicollGet("http://"+addr+"/x?t="+awkward),
		Key{}, NamedSecret{Name: "token", Value: awkward})
	fail := failed(t, ex, err)
	// The query is cleared wholesale as well, so this asserts the property
	// where it is load-bearing: the elision itself, against the encoded form.
	if got := elide("t="+"a%2Bb%2Fc+d%3De", []NamedSecret{{Name: "token", Value: awkward}}); strings.Contains(got, "a%2Bb") {
		t.Errorf("the escaped form survived elision: %s", got)
	}
	if strings.Contains(fail.Reason, awkward) {
		t.Errorf("the failure reason carries the value: %s", fail.Reason)
	}
}

// AND THE ORDINARY CASE STILL READS. A request with no secret in it is not
// redacted into uselessness — the address is what a person needs to see.
func TestRedaction_AFailureWithNoSecretStillNamesTheAddress(t *testing.T) {
	addr := deadAddress(t)
	ex, err := New().Send(t.Context(), apicollGet("http://"+addr+"/health"), Key{})
	fail := failed(t, ex, err)
	if !strings.Contains(fail.Reason, "/health") {
		t.Errorf("reason = %q, want the address in it", fail.Reason)
	}
	if !strings.Contains(fail.Reason, addr) {
		t.Errorf("reason = %q, want the host in it", fail.Reason)
	}
}

// The response body is searched for the same values on the way back, so a
// server that ECHOES a token into its error text does not put it on screen
// as ordinary text (§11.4). Asserted here beside the request-side redaction
// because the two are one property from two directions.
func TestRedaction_AnEchoedValueComesBackMarked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad token: `+carried+`"}`)
	}))
	defer srv.Close()

	ex, err := New().Send(t.Context(),
		apicollGet(srv.URL+"/bot"+carried+"/sendMessage"),
		Key{}, NamedSecret{Name: "token", Value: carried})
	got := answered(t, ex, err)
	if strings.Contains(got.Raw.Text, carried) {
		t.Errorf("the echoed value crossed in the raw response: %s", got.Raw.Text)
	}
	if !strings.Contains(got.Raw.Text, "⟦token⟧") {
		t.Errorf("the echo was not marked: %s", got.Raw.Text)
	}
}
