package apisend

// What verification says about the chain an exchange accepted
// (nocx-6hg2w.19).
//
// The run used to report the environment's SETTING — verification is off —
// which is true of every send under that environment and says nothing about
// any of them. What a person needs to know is whether anything was accepted
// that would otherwise have been REFUSED, and these are the four answers.
//
// Every case here is a real handshake against a real TLS server, because the
// question is about a chain and a chain only exists once one has completed.
// The same server appears twice under different trust — once with its
// certificate in the client's roots and once without — which is what makes
// the difference between the two answers the CHAIN's rather than the
// server's.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newInsecure is a client whose transport does not verify — an environment
// with the switch on (§6.5) — optionally trusting a test server's own
// certificate, so "the chain would have verified" and "it would not" are
// both reachable against one server.
func newInsecure(roots *tls.Config) *Client {
	c := New()
	c.tlsConfig = roots
	return c
}

// insecureKey is the key an environment with verification off produces.
var insecureKey = Key{InsecureTLS: true}

func TestTrust_VerificationOnSaysVerifiedAndRunsNoSecondPass(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	ex, err := newTrusting(trust(srv)).Send(t.Context(), apicollGet(srv.URL), Key{})
	got := answered(t, ex, err)
	if got.Trust.State != TrustVerified {
		t.Fatalf("state = %q, want verified", got.Trust.State)
	}
	// NOTHING TO REPORT is the whole of the ordinary case: a reason here
	// would be a sentence about a connection that was fine.
	if got.Trust.Reason != "" {
		t.Errorf("reason = %q on a verified chain, want empty", got.Trust.Reason)
	}
}

// THE BADGE'S CASE. Verification off, and the chain would have been refused:
// httptest's certificate is signed by a throwaway authority nothing on this
// machine has any reason to trust.
func TestTrust_VerificationOffOverAnUntrustedChainSaysWhy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	// No roots at all: the machine's own, which do not include this server.
	ex, err := newInsecure(nil).Send(t.Context(), apicollGet(srv.URL), insecureKey)
	got := answered(t, ex, err)
	if got.Trust.State != TrustUncheckedUntrusted {
		t.Fatalf("state = %q, want unchecked-untrusted", got.Trust.State)
	}
	// The verifier's own sentence, which is the sentence a person wants —
	// asserted as a real explanation rather than as any non-empty string.
	if !strings.Contains(got.Trust.Reason, "certificate") {
		t.Errorf("reason = %q, want the verifier's account of what is wrong", got.Trust.Reason)
	}
	// …and it still ANSWERED. The exchange is unaffected: this is a
	// description of what was accepted, never a second refusal.
	if got.Text != "secure" {
		t.Errorf("Text = %q, want the body — the run is unchanged by the verdict", got.Text)
	}
}

// THE PAIR, and the one that makes the test above about the CHAIN rather
// than about the switch: the same server, the same switch, its certificate
// in the roots this client would have used. Nothing was accepted that would
// not have been accepted anyway, so there is nothing to warn about.
func TestTrust_VerificationOffOverAChainThatWouldPassSaysSoQuietly(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	ex, err := newInsecure(trust(srv)).Send(t.Context(), apicollGet(srv.URL), insecureKey)
	got := answered(t, ex, err)
	if got.Trust.State != TrustUncheckedTrusted {
		t.Fatalf("state = %q, want unchecked-trusted", got.Trust.State)
	}
	if got.Trust.Reason != "" {
		t.Errorf("reason = %q where nothing was refused, want empty", got.Trust.Reason)
	}
}

// A NAME THE CERTIFICATE IS NOT FOR is refused as surely as an unknown
// authority, and it is the case a self-signed development host most often
// meets — the certificate is trusted and the host it is presented for is not
// the one on it. Asserted because the verifier is being asked the host-name
// question at all: Verify with no DNSName checks the chain and nothing else,
// which would call this connection fine.
func TestTrust_VerificationOffOverAChainForAnotherNameIsUntrusted(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	// The server's own certificate IS trusted here. What is wrong is the
	// NAME: httptest issues for example.com and 127.0.0.1, and this route
	// resolves any name to loopback — so the request is made for a name
	// nothing on that certificate carries.
	c := newInsecure(trust(srv))
	c.routes = fixedRoute(namedRoute())
	ex, err := c.Send(t.Context(), apicollGet(otherName(t, srv.URL)), insecureKey)
	got := answered(t, ex, err)
	if got.Trust.State != TrustUncheckedUntrusted {
		t.Fatalf("state = %q, want unchecked-untrusted for a name the certificate does not carry", got.Trust.State)
	}
	if !strings.Contains(got.Trust.Reason, "valid for") {
		t.Errorf("reason = %q, want it to name the mismatch", got.Trust.Reason)
	}
}

// NO TLS, NOTHING TO CLAIM. A plain http exchange presents no chain, so
// there is no verdict to give — and `none` is that said out loud rather
// than an empty state a reader has to interpret.
func TestTrust_APlainHTTPExchangeClaimsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	ex, err := New().Send(t.Context(), apicollGet(srv.URL), Key{})
	got := answered(t, ex, err)
	if got.Trust.State != TrustNone {
		t.Errorf("state = %q, want none for an exchange with no TLS in it", got.Trust.State)
	}
}

// A HANDSHAKE THAT FAILED CARRIES NO CHAIN AND CLAIMS NONE. There is no
// response at all on such a run, so there is nowhere for a verdict to be —
// and the certificate list beside it is empty, which is the same fact from
// the other side.
func TestTrust_AFailedHandshakeCarriesNoChainAndNoVerdict(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	ex, err := New().Send(t.Context(), apicollGet(srv.URL), Key{})
	failedAt(t, ex, err, PhaseTLS)
	if ex.Response != nil {
		t.Fatalf("a refused handshake produced a response: %+v", *ex.Response)
	}
	if len(ex.Certificates) != 0 {
		t.Errorf("certificates = %d for a handshake that never completed", len(ex.Certificates))
	}
}

// trustOf ITSELF, at the two edges the send path cannot reach: an empty
// chain, and a chain whose leaf is expired. The rest of this file drives it
// through a real handshake, which is the right level for the ordinary
// cases; these two are shapes a live server cannot easily be made to have.
func TestTrust_TheVerdictAtItsEdges(t *testing.T) {
	c := New()

	if got := c.trustOf(nil, "example.com", true); got.State != TrustNone {
		t.Errorf("state = %q for an empty chain, want none", got.State)
	}
	// Verification ON with a chain present answers `verified` WITHOUT
	// looking at the chain — the handshake already did, and this is the
	// assertion that no second pass runs: an expired certificate that would
	// fail a verification is still reported verified here, because the
	// connection it describes did verify at the moment it was made.
	expired := expiredChain(t)
	if got := c.trustOf(expired, "example.com", false); got.State != TrustVerified {
		t.Errorf("state = %q with verification on, want verified with no second pass", got.State)
	}
	if got := c.trustOf(expired, "example.com", true); got.State != TrustUncheckedUntrusted {
		t.Errorf("state = %q for an expired chain with verification off, want unchecked-untrusted", got.State)
	} else if !strings.Contains(got.Reason, "expired") {
		t.Errorf("reason = %q, want it to say the certificate has expired", got.Reason)
	}
}

// otherName rewrites a test server URL to a name its certificate does NOT
// carry. byName next door does the opposite — the name it IS valid for —
// and the two exist for the two halves of the same question.
func otherName(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	u.Host = "not-on-the-certificate.test:" + u.Port()
	return u.String()
}

// expiredChain mints one self-signed certificate whose validity ended
// yesterday — a shape httptest cannot be asked for.
func expiredChain(t *testing.T) []*x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		DNSNames:     []string{"example.com"},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     time.Now().Add(-24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return []*x509.Certificate{cert}
}
