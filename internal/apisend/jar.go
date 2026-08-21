package apisend

// The cookie jar, and the two decisions it carries.
//
// # It is per CookieScope, which is why the scope is in the Key
//
// A login that sets a cookie must be followed by a request that carries it,
// with no configuration at all — that is the whole feature, and it is one
// line of net/http. What is not one line is keeping two environments apart
// while they are IN FLIGHT AT ONCE. A single shared client cannot hold a
// per-environment jar and a per-call route at the same time: whichever is
// set last wins, and the loser's request goes out with the other
// environment's session, or through the other environment's bastion.
//
// So the instance is what varies, it is keyed by exactly what varies, and
// it is immutable once built (client.go). The jar belongs to the instance;
// the scope is in the Key because the jar is.
//
// # It does not survive a restart, and that is deliberate
//
// A session cookie IS credential material. The only place this feature
// keeps credential material at rest is the vault behind the binding
// document (design §8, internal/apibind), and a jar file would be a second
// store of credentials — on disk, outside the vault, guarded by nothing,
// holding exactly the token an attacker wants and never named in §8's
// threat model. That alone decides it.
//
// Two further reasons point the same way. A cookie with no Max-Age is
// defined to die with the session; a jar that outlived the process would
// silently promote every one of them to a persistent cookie the server
// never asked for, which is our surface lying about the server's. And the
// cost of NOT persisting is one re-run of a login request — which in this
// product is a first-class object the user already has, in the collection,
// one click away. Persisting buys a click and pays for it in credentials at
// rest.
//
// The interval, both ends: a cookie exists from the response that stored it
// until the server expires it or THIS PROCESS EXITS. There is no third way
// for one to disappear, and no way at all for one to appear that a response
// did not set. Pinned by TestJar_DoesNotSurviveARestart, which uses an
// explicitly long-lived cookie so that nothing but the restart can be what
// dropped it.
//
// What this does NOT do, named rather than left to be found: closing a
// collection does not empty its jar. The scope is the collection's stable
// path, so closing and reopening it resumes the same session — which is
// what a user reopening a collection expects, and which is also why there
// is no "log out" here. When there is a reason for one, it is a method on
// this seam and not a second jar.

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
)

// newJar builds the jar for one cookie scope.
//
// The scope is not consulted: it identifies the jar rather than
// parameterising it, and the isolation comes from there being one jar per
// instance per scope. It is taken as an argument so that the failure names
// the scope whose sends are about to fail, because "cookie jar: …" with no
// subject is a message nobody can act on.
//
// nil PublicSuffixList is the deliberate reading of net/http's warning:
// without one, the jar is vulnerable to a supercookie set by a server for
// a broad suffix like "co.uk". The alternative is a dependency on a suffix
// table that has to be updated forever, and the exposure here is a request
// the user typed, to a server the user named, in a jar that lives only as
// long as the process and is shared with nothing else in the app. A shared
// browser jar would be a different answer to a different question.
func newJar(scope string) (http.CookieJar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("%s: cookie jar for scope %q: %w", component, scope, err)
	}
	return jar, nil
}
