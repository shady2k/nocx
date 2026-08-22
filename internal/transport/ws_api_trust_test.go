package transport

// What the run says about the chain it accepted (nocx-6hg2w.19), off the
// real socket.
//
// The run used to report the environment's SETTING, so `unverified TLS`
// appeared on every send under an environment with verification off —
// including a public host with an ordinary chain. What crosses now is the
// verifier's ANSWER, and these are the states it can be.
//
// ONE STATE IS NOT HERE, and it is worth saying why rather than leaving a
// reader to notice: `unchecked-trusted` — verification off over a chain that
// would have verified anyway — needs the sender to trust the test server's
// own certificate, and the sender's root pool has no Option by design (see
// apisend.Client: the config is set directly by that package's own tests,
// and giving the product a knob it has no surface for is what its doc
// refuses). So that state is proved by a real handshake in
// internal/apisend/trust_test.go, and everything reachable from this side is
// here.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// trustOf decodes one send and returns what the run says about the chain.
func trustOfRun(t *testing.T, resp *vaultRPCResult) apiTrustWire {
	t.Helper()
	got := decodeSend(t, resp)
	if got.Response == nil {
		t.Fatalf("the run carries no response, so nothing to read a verdict off: %+v", got)
	}
	return got.Response.Trust
}

// A SELF-SIGNED SERVER UNDER AN INSECURE ROUTE: the case the badge is for.
// httptest's certificate is signed by a throwaway authority nothing on this
// machine trusts, so the run says what was accepted and why it would not
// have been.
func TestAPIRequestSend_AnUnverifiedChainIsNamedOnTheRun(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiConnectionFolder(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	trust := trustOfRun(t, vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "ping.json",
		"envRelPath": "environments/unverified.json", "token": "t-1",
	}, 2))

	if trust.State != "unchecked-untrusted" {
		t.Fatalf("state = %q, want unchecked-untrusted", trust.State)
	}
	if trust.Reason == "" {
		t.Error("the run says a chain was accepted untrusted and does not say why")
	}
}

// THE SAME SERVER WITH VERIFICATION ON is refused at the handshake, so there
// is no response at all and no chain to describe. The pair that keeps the
// test above about the SWITCH rather than about the server.
func TestAPIRequestSend_TheSameServerVerifiedIsRefusedAndClaimsNoChain(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}))
	defer srv.Close()

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiConnectionFolder(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	got := decodeSend(t, vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "ping.json",
		"envRelPath": "environments/here.json", "token": "t-1",
	}, 2))

	if got.Outcome != "failed" {
		t.Fatalf("outcome = %q, want failed — an untrusted chain is refused when the switch is off", got.Outcome)
	}
	if got.Failure == nil || got.Failure.Phase != "tls" {
		t.Fatalf("failure = %+v, want phase tls", got.Failure)
	}
	if got.Response != nil {
		t.Errorf("a refused handshake produced a response: %+v", *got.Response)
	}
	if len(got.Certificates) != 0 {
		t.Errorf("certificates = %d for a handshake that never completed", len(got.Certificates))
	}
}

// AND A PLAIN HTTP RUN CLAIMS NOTHING, under the very environment whose
// switch is on: the setting is not the answer, which is the whole change.
func TestAPIRequestSend_APlainExchangeUnderAnInsecureRouteClaimsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	_, conn := newAPIWSServer(t, newAPIFakeBindings())
	root := apiConnectionFolder(t, srv.URL)
	handle := openAPICollection(t, conn, root, 1)

	resp := vaultCall(t, conn, "api.request.send", map[string]any{
		"handle": handle, "relPath": "ping.json",
		"envRelPath": "environments/unverified.json", "token": "t-1",
	}, 2)
	trust := trustOfRun(t, resp)

	if trust.State != "none" {
		t.Errorf("state = %q, want none — there was no chain to accept", trust.State)
	}
	// And the run still reports the environment's setting on its route, which
	// is a different fact and stays a fact: what changed is that nothing
	// draws a warning from it.
	got := decodeSend(t, resp)
	if !got.Route.InsecureTLS {
		t.Error("route.insecureTls = false; the environment's own setting still belongs on the run")
	}
}

// The schema refuses a trust nobody declared — the negative that makes the
// closed set closed.
func TestAPIRequestSend_TheContractRefusesAnUndeclaredTrustState(t *testing.T) {
	schema := loadSchema(t, "api.request.send.schema.json")
	for name, state := range map[string]string{
		"a state nobody declared": `"insecure"`,
		"an empty state":          `""`,
		"no trust at all":         ``,
	} {
		t.Run(name, func(t *testing.T) {
			trust := `"trust":{"state":` + state + `,"reason":""},`
			if state == `` {
				trust = ``
			}
			raw := `{"outcome":"answered","request":{"text":"","spans":[]},"response":{"status":200,` +
				`"headers":[],"text":"","binary":false,"lossy":false,"truncated":false,"size":0,` +
				`"tlsVersion":"","tlsCipherSuite":"",` + trust +
				`"raw":{"text":"","spans":[]}},"failure":null,"environment":"",` +
				`"route":{"kind":"direct","profileId":"","insecureTls":false},"remoteAddr":"",` +
				`"dnsAddresses":[],"timings":{"dnsMs":0,"connectMs":0,"tlsMs":0,"ttfbMs":0,"totalMs":0},` +
				`"certificates":[]}`
			if err := validateJSONErr(schema, []byte(raw)); err == nil {
				t.Fatalf("the schema accepted %s", raw)
			}
		})
	}
}
