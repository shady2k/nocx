package apisend

// A SECRET IN THE ADDRESS REFUSES A CROSS-ORIGIN REDIRECT (nocx-ew3uv.3).
//
// The credential rule strips Authorization, the cookies and the endpoint's
// custom headers on an origin change, because a header value can BE a
// credential. That rule works by DELETION, and a value in a path or a query
// cannot be deleted — it IS the target. So the same rule applied honestly
// refuses the hop rather than choosing between handing a token to an origin
// nobody named and asking for a resource nobody meant.
//
// This is a deliberate decision and not a limitation to work around: the
// alternative that "just works" is the one that leaks.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

//nolint:gosec // a test fixture: the point is that it looks like a token
const urlToken = "sk-live-9f2c4e7a11b3d8"

// TestSend_ASecretInThePathRefusesACrossOriginRedirect: the far side is
// never reached, the reason names the origin it would have gone to, and the
// value is in none of it.
func TestSend_ASecretInThePathRefusesACrossOriginRedirect(t *testing.T) {
	var elsewhereHits atomic.Int64
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhereHits.Add(1)
		_, _ = io.WriteString(w, "should never be served")
	}))
	defer elsewhere.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/moved", http.StatusFound)
	}))
	defer origin.Close()

	ex, err := New().Send(t.Context(),
		apicollGet(origin.URL+"/bot"+urlToken+"/sendMessage"),
		Key{}, NamedSecret{Name: "token", Value: urlToken})
	fail := failed(t, ex, err)

	// THE HOP DID NOT HAPPEN.
	if n := elsewhereHits.Load(); n != 0 {
		t.Fatalf("the other origin was reached %d times — a token in a path followed a redirect", n)
	}
	// IT NAMES WHERE IT WOULD HAVE GONE, so a person can see what they were
	// being sent to rather than only that something stopped.
	if !strings.Contains(fail.Reason, elsewhere.URL) {
		t.Errorf("reason = %q, want it to name the origin the redirect pointed at", fail.Reason)
	}
	// AND NEVER THE VALUE — not in the reason, not in the request text.
	if strings.Contains(fail.Reason, urlToken) {
		t.Fatalf("the refusal carries the value: %s", fail.Reason)
	}
	if strings.Contains(ex.Request.Text, urlToken) {
		t.Fatalf("the request text carries the value: %s", ex.Request.Text)
	}
	// The run still shows what was sent, with the value elided — the whole
	// point of the exchange being a run rather than an error.
	if !strings.Contains(ex.Request.Text, "⟦token⟧") {
		t.Errorf("the run does not mark where the secret went:\n%s", ex.Request.Text)
	}
}

// A SAME-ORIGIN REDIRECT IS STILL FOLLOWED. Without this the rule above
// would be satisfied by a build that refused every redirect a secret-bearing
// request met — which is a different, worse feature.
func TestSend_ASecretInThePathStillFollowsASameOriginRedirect(t *testing.T) {
	var served atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/bot"+urlToken+"/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		_, _ = io.WriteString(w, "arrived")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ex, err := New().Send(t.Context(),
		apicollGet(srv.URL+"/bot"+urlToken+"/sendMessage"),
		Key{}, NamedSecret{Name: "token", Value: urlToken})
	got := answered(t, ex, err)
	if got.Text != "arrived" {
		t.Fatalf("Text = %q, want the followed hop's body", got.Text)
	}
	if n := served.Load(); n != 1 {
		t.Errorf("the final path was served %d times, want 1", n)
	}
}

// A REQUEST WITH NO SECRET IN ITS ADDRESS follows a cross-origin redirect
// exactly as it always did — the credential rule strips the header and the
// hop proceeds. The pair that keeps the refusal about the ADDRESS rather
// than about redirects.
func TestSend_ACrossOriginRedirectWithNoSecretInTheAddressStillFollows(t *testing.T) {
	var arrived atomic.Int64
	var sawAuth atomic.Value
	sawAuth.Store("")
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived.Add(1)
		sawAuth.Store(r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, "followed")
	}))
	defer elsewhere.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/moved", http.StatusFound)
	}))
	defer origin.Close()

	// The secret is in a HEADER here, which is the case the deletion rule
	// covers: the hop happens and the credential does not go with it.
	r := apicoll.Request{
		Method:  http.MethodGet,
		URL:     origin.URL + "/plain",
		Headers: []apicoll.Header{{Name: "Authorization", Value: "Bearer " + urlToken, Enabled: true}},
	}
	ex, err := New().Send(t.Context(), r, Key{}, NamedSecret{Name: "token", Value: urlToken})
	got := answered(t, ex, err)
	if got.Text != "followed" {
		t.Fatalf("Text = %q, want the redirect to have been followed", got.Text)
	}
	if n := arrived.Load(); n != 1 {
		t.Errorf("the other origin was reached %d times, want 1", n)
	}
	if h, _ := sawAuth.Load().(string); h != "" {
		t.Errorf("the credential followed the hop as %q — the header rule did not fire", h)
	}
}
