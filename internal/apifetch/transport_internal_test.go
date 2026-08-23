package apifetch

import (
	"testing"

	"github.com/shady2k/nocx/internal/httppolicy"
)

// The structural half of "a fetch is not an environment": whatever the
// route says, the transport this builds carries no TLS configuration of its
// own, so it verifies the way Go verifies. The behavioural half — an
// untrusted certificate refused even for a route with InsecureTLS set — is
// in fetch_test.go; this one is here because a future edit that adds a
// TLSClientConfig would pass every black-box test that does not happen to
// use https.
func TestTransport_CarriesNoTLSConfiguration(t *testing.T) {
	tr := New(nil, nil).transport(httppolicy.Local())
	if tr.Inner.TLSClientConfig != nil {
		t.Errorf("the fetch transport carries a TLSClientConfig (%+v); it must carry none",
			tr.Inner.TLSClientConfig)
	}
}
