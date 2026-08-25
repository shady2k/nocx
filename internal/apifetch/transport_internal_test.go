package apifetch

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/shady2k/nocx/internal/httppolicy"
)

// stubRoute is a route of a shape neither Local nor nil: what a connection
// lease produces, resolving on the far side and dialling its own way.
type stubRoute struct{}

func (stubRoute) LookupIP(context.Context, string) ([]net.IP, error) { return nil, nil }

func (stubRoute) DialContext(context.Context, string, string) (net.Conn, error) { return nil, nil }

func (stubRoute) ProxyForHTTPS(*http.Request) (*url.URL, error) { return nil, nil }

// The structural half of "a fetch is not an environment": whatever the
// route, the transport this builds carries no TLS configuration of its own,
// so it verifies the way Go verifies. ON ANY ROUTE, EVER is the claim, so
// the table is the assertion and not one route standing for the rest — the
// edit this guards against is a `TLSClientConfig` reached for on the
// connection route only, which a single-route test would sail past.
//
// The behavioural halves — an untrusted certificate refused even for a route
// with InsecureTLS set, over the direct route AND over a connection — are in
// fetch_test.go; this one is here because a future edit that adds a
// TLSClientConfig would pass every black-box test that does not happen to
// use https.
func TestTransport_CarriesNoTLSConfigurationOnAnyRoute(t *testing.T) {
	for name, r := range map[string]httppolicy.Route{
		"nil (the policy's own default)": nil,
		"direct":                         httppolicy.Local(),
		"a connection's own route":       stubRoute{},
	} {
		t.Run(name, func(t *testing.T) {
			tr := New(nil, nil).transport(r)
			if tr == nil || tr.Inner == nil {
				t.Fatal("no transport was built; the assertion below would be vacuous")
			}
			if tr.Inner.TLSClientConfig != nil {
				t.Errorf("the fetch transport carries a TLSClientConfig (%+v); it must carry none",
					tr.Inner.TLSClientConfig)
			}
		})
	}
}
