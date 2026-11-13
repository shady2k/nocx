// Package connkey is the identity one SSH connection is shared under.
//
// One connection per (host, port, user, credential principal, route): it is
// AD-4's pool key, the connection supervisor's state key, and the identity a
// pane is bound to. It lives in its own package because the two ends that need
// it cannot import each other — internal/ssh is the transport and
// internal/hostconn is the supervisor above it — and two spellings of one
// identity is precisely the defect nocx-oalo1.4.4 exists to remove: two
// connections to one host under different credentials would share a state, or
// one connection would split in two because two derivations disagreed about
// the route.
//
// The DERIVATION lives in one function, internal/ssh's `PooledSpec.Key`, which
// also answers every hop's key (`RouteKeys`). This package holds the shape, so
// that the pool, the supervisor and the carrier pass one value rather than
// three copies of one rule.
package connkey

import "fmt"

// Key identifies one connection.
//
// Every component is load-bearing: dropping the identity or the route is an
// authorization bug rather than a resource bug (AD-4's key comment carries the
// full argument), and adding one that does not distinguish two principals only
// splits one connection in two.
type Key struct {
	// Host and Port are the resolved endpoint, never an alias.
	Host string
	Port int
	// User is the resolved account on that endpoint.
	User string
	// Identity is the credential principal: a stored credential's reference, a
	// public key's fingerprint, or a queue of them. Two principals on one host
	// are two connections.
	Identity string
	// Route is the rendered route the connection is dialled through, empty for
	// a direct one.
	Route string
}

// String names the connection in logs and errors without the route, which is
// usually empty and always long.
func (k Key) String() string { return fmt.Sprintf("%s@%s:%d/%s", k.User, k.Host, k.Port, k.Identity) }
