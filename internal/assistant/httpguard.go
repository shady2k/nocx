package assistant

// The assistant's HTTP client is the shared policy engine bound to the
// assistant's own concrete route: the machine's resolver and a plain
// net.Dialer. The rule itself — http:// only to loopback and private
// addresses, enforced on every connection and every redirect hop, and the
// credential dropped on any origin change — lives in internal/httppolicy,
// where its four reasons are written out in full. Read them there before
// changing anything here: a reader who does not know them will "simplify"
// this into a form validator.
//
// The extraction (design §7.3) moved the rule and left the route. It had to
// be that cut: the API-testing executor sends through an SSH connection,
// where the hostname is resolved by the remote server, so the concrete
// resolve-and-dial below could not be shared — only the policy could. This
// constructor stays locked to the local route, which is what the assistant
// has always done.

import (
	"context"
	"net"
	"net/http"
	"net/url"

	"github.com/shady2k/nocx/internal/httppolicy"
	"github.com/shady2k/nocx/internal/log"
)

// component prefixes every refusal the policy raises for the assistant, so
// the message names something the user recognises.
const component = "assistant"

// guardedTransport is the assistant's handle on the policy transport. inner
// and proxy are the transport's own inner transport and proxy decision,
// reachable here because a caller that owns the client legitimately
// configures its TLS and legitimately asks what the proxy decision was.
type guardedTransport struct {
	*httppolicy.Transport

	inner *http.Transport
	proxy func(*http.Request) (*url.URL, error)
}

// newGuardedHTTPClient builds the http.Client every model call goes through.
// logger may be nil (tests).
func newGuardedHTTPClient(logger log.Logger) *http.Client {
	return newGuardedHTTPClientWithResolver(logger, nil)
}

func newGuardedHTTPClientWithResolver(logger log.Logger, resolve func(ctx context.Context, host string) ([]net.IP, error)) *http.Client {
	resolver := httppolicy.SystemResolver()
	if resolve != nil {
		resolver = httppolicy.ResolverFunc(resolve)
	}
	// The assistant's route, unchanged by the extraction: this machine
	// resolves, this machine dials, and https keeps the environment proxy.
	route := httppolicy.NewRoute(resolver, &net.Dialer{}, httppolicy.EnvironmentProxy)

	pt := httppolicy.NewTransport(httppolicy.Params{
		Component: component,
		Route:     route,
		Log:       logger,
	})
	tr := &guardedTransport{Transport: pt, inner: pt.Inner, proxy: pt.Proxy}
	return &http.Client{
		Transport:     tr,
		CheckRedirect: pt.CheckRedirect,
	}
}

// withCustomHeaderNames tags ctx with the canonical names of the custom
// headers the request carries. Set by the request builders (engine.go for
// the completion, connection.go for the connection check) so the guard never
// has to guess which headers are the endpoint's.
func withCustomHeaderNames(ctx context.Context, names []string) context.Context {
	return httppolicy.WithCustomHeaderNames(ctx, names)
}
