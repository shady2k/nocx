package apisend

// The seam of design §7.1: one executor, a supplied dialer. Locally the
// transport dials with net.Dialer; through a connection it dials with a
// lease on the SSH pool. Local and remote are NOT two strategies — they are
// one executor with a different route, which is AD-8's form: variance
// expressed by the interface, no flag inside the implementation, and a third
// route added without editing a switch.

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/httppolicy"
)

// Dialer opens one connection for a route. It is aliased rather than
// redeclared: the transport that calls it lives in internal/httppolicy, and
// one behaviour has one owner. A second declaration of this shape would be
// two answers to one question that agree until they do not.
type Dialer = httppolicy.Dialer

// Route is a Dialer plus the thing a Dialer alone cannot answer: WHO
// RESOLVES THE NAME. The http:// address rule is about the resolved address
// (reason 3 in httppolicy), so a route that dials on a remote host must also
// report the addresses that host will reach — the policy validates exactly
// what the route returns and then dials exactly that.
type Route = httppolicy.Route

// Routes answers "which route is this?" for the RouteID on a Key. The route
// lives on the ENVIRONMENT, never on a request (design §6.5), so the id is
// the environment's route and the table that maps it is the environment
// store's, not the sender's.
type Routes func(ctx context.Context, routeID string) (Route, error)

// directRoutes is the table until the environment store supplies one: the
// empty id is this machine, and any other id is refused by name rather than
// silently downgraded to a direct send. A request the user routed through a
// connection must never quietly go out of this machine's own interface.
func directRoutes(_ context.Context, routeID string) (Route, error) {
	if routeID == "" {
		return httppolicy.Local(), nil
	}
	return nil, fmt.Errorf("apisend: no route named %q — this sender knows only the direct route", routeID)
}
