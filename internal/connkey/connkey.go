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

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
)

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

// ID is the connection's public name: what a pane is bound to and what a
// connection event is keyed by.
//
// IT IS A DIGEST AND NOT String(), and the difference is load-bearing in two
// ways. String() deliberately omits the route, so two connections reached
// through different bastions would share it — an authorization mistake, not a
// cosmetic one. And it carries the account and the credential principal, which
// a renderer has no business reading. A digest over the full identity, spelled
// with lengths so that no two component lists can render alike, is the same
// identity the pool and the supervisor key a connection by, in a form that is
// safe to publish and stable across processes.
//
// Stable is what makes it usable: a coordinator rebinding a pane after a
// restart, or a second coordinator asking about the same destination, computes
// the same id, because it is derived from the key and nothing else.
func (k Key) ID() string {
	h := sha256.New()
	write := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	write(k.Host)
	// The port joins the digest as its decimal spelling, not as eight bytes:
	// an int cannot be converted to uint64 without the possibility of a
	// negative value becoming a small positive one, and the spelling is
	// injective for the values a port can hold without that conversion at all.
	write(strconv.Itoa(k.Port))
	write(k.User)
	write(k.Identity)
	write(k.Route)
	return hex.EncodeToString(h.Sum(nil))[:32]
}
